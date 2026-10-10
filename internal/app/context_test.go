package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Iliorn/tjek/todo"
)

func activeTitles(m model) []string {
	var out []string
	for _, t := range m.cache.active {
		out = append(out, t.Title)
	}
	return out
}

// The whole round: filter with /, save it under a name with F, and the
// context stays on under a new / filter, in the status line, in the Projects
// counts, and across a restart; "no context" switches it off, and del deletes
// it.
func TestContextSavedFromTheFilterStaysOn(t *testing.T) {
	mk := func(title, project string, p todo.Priority, tags ...string) todo.Todo {
		x := todo.New(title)
		x.Project, x.Priority, x.Tags = project, p, tags
		return x
	}
	m := modelWithTasks(t,
		mk("fix login", "Site", todo.PriorityHigh, "work"),
		mk("write report", "Site", todo.PriorityLow, "work"),
		mk("water plants", "Garden", todo.PriorityHigh, "home"),
	)
	m.termWidth, m.termHeight = 110, 30

	// With no filter there is nothing to save yet.
	m = script(t, m, "F", "work")
	if rows := m.contextRows(); len(rows) != 1 || rows[0].kind != contextRowNeedFilter {
		t.Fatalf("rows without a filter = %+v, want the need-a-filter row", rows)
	}
	m = script(t, m, "esc", "/", "#work", "enter", "F", "work", "enter")
	if m.mode != modeNormal || m.context != "work" || m.searchQuery != "" {
		t.Fatalf("after saving: mode %v, context %q, search %q", m.mode, m.context, m.searchQuery)
	}
	m.ensureCache()
	if got := activeTitles(m); !slices.Equal(got, []string{"Fix login", "Write report"}) {
		t.Errorf("context work shows %v", got)
	}
	if !strings.Contains(ansi.Strip(m.View()), "◉ work") {
		t.Error("the status line should name the context")
	}
	if g := m.cache.projectGroups["Garden"]; g != nil && g.open > 0 {
		t.Errorf("Garden counts %d open tasks outside the context", g.open)
	}
	for _, x := range m.statsScopedTodos() {
		if x.Project == "Garden" {
			t.Error("Stats counts a task outside the context")
		}
	}

	// A / filter narrows inside the context.
	m = script(t, m, "/", "p:high", "enter")
	m.ensureCache()
	if got := activeTitles(m); !slices.Equal(got, []string{"Fix login"}) {
		t.Errorf("context work with p:high shows %v", got)
	}

	// The choice is kept.
	s, _ := loadSettings()
	if s.Context != "work" || len(s.Contexts) != 1 || s.Contexts[0].Query != "#work" {
		t.Errorf("settings kept context %q, contexts %+v", s.Context, s.Contexts)
	}

	// "no context" is the first row while one is on.
	m = script(t, m, "esc", "F", "enter")
	if m.context != "" {
		t.Errorf("no context row left %q on", m.context)
	}
	m.ensureCache()
	if len(m.cache.active) != 3 {
		t.Errorf("with no context the list has %v", activeTitles(m))
	}

	// del deletes the context under the cursor.
	m = script(t, m, "F", "delete", "esc")
	if len(m.contexts) != 0 {
		t.Errorf("contexts after del = %+v", m.contexts)
	}
}

// The tag and project pickers offer every name, also those whose tasks are
// all waiting or outside the active context.
func TestPickersOfferHiddenTagsAndProjects(t *testing.T) {
	later := todo.New("later task")
	later.Tags, later.Project = []string{"someday-tag"}, "Later"
	later.SetStartDate(todo.Someday)
	other := todo.New("other task")
	other.Tags, other.Project = []string{"elsewhere"}, "Elsewhere"
	here := todo.New("here task")
	here.Tags = []string{"here"}
	m := modelWithTasks(t, later, other, here)
	m.contexts = []savedContext{{Name: "here", Query: "#here"}}
	m.context = "here"
	m.refreshCaches()
	for _, tag := range []string{"someday-tag", "elsewhere", "here"} {
		if !slices.Contains(m.getAllTagsSorted(), tag) {
			t.Errorf("tag picker lacks %q: %v", tag, m.getAllTagsSorted())
		}
	}
	for _, p := range []string{"Later", "Elsewhere"} {
		if !slices.Contains(m.cache.projectNames, p) {
			t.Errorf("project picker lacks %q: %v", p, m.cache.projectNames)
		}
	}
}

// Waiting tasks show only through a group that names "waiting": in the search
// ("#work or waiting" keeps waiting #work tasks out of the #work group) or in
// the context (a context of "waiting" shows them with nothing in /). The
// status line counts the waiting tasks inside the context.
func TestWaitingTasksShowOnlyThroughAGroupThatAsks(t *testing.T) {
	mk := func(title, tag string, waits bool) todo.Todo {
		x := todo.New(title)
		x.Tags = []string{tag}
		if waits {
			x.SetStartDate(todo.Someday)
		}
		return x
	}
	m := modelWithTasks(t,
		mk("work now", "work", false),
		mk("work later", "work", true),
		mk("home later", "home", true),
	)
	m.searchQuery = "#work or waiting -#work"
	m.refreshCaches()
	if got := activeTitles(m); !slices.Equal(got, []string{"Work now", "Home later"}) &&
		!slices.Equal(got, []string{"Home later", "Work now"}) {
		t.Errorf("#work or waiting -#work shows %v; the #work group must not bring in Work later", got)
	}

	m.searchQuery = ""
	m.contexts = []savedContext{{Name: "parked", Query: "waiting"}}
	m.context = "parked"
	m.refreshCaches()
	if got := activeTitles(m); len(got) != 2 || slices.Contains(got, "Work now") {
		t.Errorf("context waiting shows %v, want the two waiting tasks", got)
	}

	m.contexts = []savedContext{{Name: "home", Query: "#home"}}
	m.context = "home"
	m.refreshCaches()
	if m.cache.waitingTop != 1 {
		t.Errorf("inside #home the status line counts %d waiting, want 1", m.cache.waitingTop)
	}
}
