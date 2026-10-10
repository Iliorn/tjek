package app

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
	"github.com/charmbracelet/x/ansi"
)

func TestFormatSince(t *testing.T) {
	applyLang(string(langEN))
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.Local)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, "-"},
		{now.Add(-time.Hour), "today"},
		{now.AddDate(0, 0, -1), "1d"},
		{now.AddDate(0, 0, -13), "13d"},
		{now.AddDate(0, 0, -14), "2w"},
		{now.AddDate(0, 0, -69), "9w"},
		{now.AddDate(0, 0, -95), "3mo"},
	}
	for _, c := range cases {
		if got := formatSince(c.at, now); got != c.want {
			t.Errorf("formatSince(%v) = %q, want %q", c.at, got, c.want)
		}
	}
	// Across the spring clock change, yesterday is 23 hours of midnights back.
	if cph, err := time.LoadLocation("Europe/Copenhagen"); err == nil {
		now := time.Date(2027, 3, 29, 10, 0, 0, 0, cph)
		if got := formatSince(time.Date(2027, 3, 28, 12, 0, 0, 0, cph), now); got != "1d" {
			t.Errorf("a move the day the clocks went forward reads %q the day after, want 1d", got)
		}
	}
}

// One pass builds every group's counts. An untagged subtask belongs to its
// parent and so to no tag group.
func TestSummarizeGroups(t *testing.T) {
	now := time.Now()
	parent := mkTodo("p", "parent", todo.Pending)
	parent.Tags = []string{"home"}
	child := mkTodo("c", "child", todo.Pending)
	child.ParentID = "p"
	late := mkTodo("l", "late", todo.Pending)
	late.Tags = []string{"home"}
	late.DueDate = now.AddDate(0, 0, -2)
	done := mkTodo("d", "done", todo.Done)
	done.Tags = []string{"home"}
	bare := mkTodo("b", "bare", todo.Pending)
	all := todoPtrs([]todo.Todo{parent, child, late, done, bare})

	sums := summarizeGroups(all, tagGroupKeys)

	home := sums["home"]
	if home.open != 2 || home.overdue != 1 || home.done != 1 {
		t.Errorf("home = %d open, %d overdue, %d done; want 2, 1, 1", home.open, home.overdue, home.done)
	}
	if u := sums[untaggedKey]; u == nil || u.open != 1 {
		t.Errorf("untagged = %+v, want only the top-level bare task", u)
	}
}

func TestVisibleGroupsHidesFinishedButKeepsThePinnedOne(t *testing.T) {
	now := time.Now()
	sums := map[string]*groupSummary{
		"busy":      {open: 3, last: now.Add(-time.Hour)},
		"quiet":     {open: 1, last: now},
		"finished":  {done: 4, last: now},
		untaggedKey: {open: 1},
	}
	all := func(string) bool { return true }

	if got, want := visibleGroups(sums, groupSortOpen, false, "", all), []string{"busy", "quiet", untaggedKey}; !reflect.DeepEqual(got, want) {
		t.Errorf("by open work = %v, want %v", got, want)
	}
	if got, want := visibleGroups(sums, groupSortRecent, false, "", all), []string{"quiet", "busy", untaggedKey}; !reflect.DeepEqual(got, want) {
		t.Errorf("by recent = %v, want %v", got, want)
	}
	if got, want := visibleGroups(sums, groupSortName, true, "", all), []string{untaggedKey, "busy", "finished", "quiet"}; !reflect.DeepEqual(got, want) {
		t.Errorf("by name, all shown = %v, want %v", got, want)
	}
	if got := visibleGroups(sums, groupSortName, false, "finished", all); !reflect.DeepEqual(got, []string{untaggedKey, "busy", "finished", "quiet"}) {
		t.Errorf("the pinned group must stay listed, got %v", got)
	}
	if n := hiddenFinishedGroups(sums, false, all); n != 1 {
		t.Errorf("hidden = %d, want 1", n)
	}
}

func TestHasDatedOpenTask(t *testing.T) {
	undated := mkTodo("a", "a", todo.Pending)
	doneDated := mkTodo("b", "b", todo.Done)
	doneDated.DueDate = time.Now()
	if hasDatedOpenTask([]todo.Todo{undated, doneDated}) {
		t.Error("only a done task has a date; the timeline has nothing ahead to draw")
	}
	started := mkTodo("c", "c", todo.Pending)
	started.StartDate = time.Now()
	if !hasDatedOpenTask([]todo.Todo{undated, started}) {
		t.Error("an open task with a start date belongs on a timeline")
	}
}

// The pane under a list shows how much of the group is done as a small bar,
// groupBarWidth cells whatever the window, followed by the percentage.
func TestGroupPaneShowsAProgressBar(t *testing.T) {
	applyLang(string(langEN))
	for _, width := range []int{60, 120, 200} {
		m := newTagModel()
		m.termWidth = width
		head := m.groupPaneHead(&groupSummary{open: 1, done: 3}, width-8)
		if len(head) != 2 {
			t.Fatalf("width %d: pane head = %q, want counts then bar", width, head)
		}
		bar := ansi.Strip(head[1])
		if !strings.HasSuffix(bar, " 75%") {
			t.Errorf("width %d: bar line = %q, want it to end in the percentage", width, bar)
		}
		if got := ansi.StringWidth(bar); got != 2+groupBarWidth+len("  75%") {
			t.Errorf("width %d: bar line is %d cells, want a fixed %d-cell bar", width, got, groupBarWidth)
		}
	}
	if head := newTagModel().groupPaneHead(&groupSummary{}, 80); len(head) != 1 {
		t.Errorf("an empty group has no bar to draw, got %q", head)
	}
}

// kitchenProject is a project whose tasks wait on each other: two chains that
// meet, a task waiting on work in another project, and one that waits on
// nothing.
func kitchenProject(t *testing.T) model {
	t.Helper()
	mk := func(id, title, project string, deps ...string) todo.Todo {
		x := todo.New(title)
		x.ID = id
		x.Project = project
		x.Dependencies = deps
		return x
	}
	return modelWithTasks(t,
		mk("measure", "Measure the kitchen", "Kitchen"),
		mk("choose", "Choose cabinets", "Kitchen", "measure"),
		mk("order", "Order cabinets", "Kitchen", "choose"),
		mk("demolish", "Tear out the old kitchen", "Kitchen"),
		mk("fit", "Fit cabinets", "Kitchen", "order", "demolish"),
		mk("lamps", "Buy lamps", "Kitchen"),
		mk("wire", "Rewire the room", "House"),
		mk("lights", "Hang the lights", "Kitchen", "lamps", "wire"),
	)
}

// A task comes one step after the latest thing it waits on, and work it
// waits on outside the list puts it at step 2 at least.
func TestGroupStepsFollowTheDependencies(t *testing.T) {
	m := kitchenProject(t)
	var roots []*todo.Todo
	for _, task := range m.allTodos() {
		if task.Project == "Kitchen" {
			roots = append(roots, m.get(task.ID))
		}
	}
	steps := groupSteps(roots, m.get)
	for id, want := range map[string]int{
		"measure": 1, "demolish": 1, "lamps": 1,
		"choose": 2, "order": 3, "fit": 4,
		"lights": 2, // lamps is step 1, and the wiring is in another project
	} {
		if steps[id] != want {
			t.Errorf("step of %s = %d, want %d", id, steps[id], want)
		}
	}

	// A cycle is cut rather than walked forever.
	a, b := todo.New("a"), todo.New("b")
	a.Dependencies, b.Dependencies = []string{b.ID}, []string{a.ID}
	cyc := groupSteps([]*todo.Todo{&a, &b}, func(string) *todo.Todo { return nil })
	if cyc[a.ID] < 1 || cyc[b.ID] < 1 {
		t.Errorf("a cycle left a task unnumbered: %v", cyc)
	}
}

// A project's list is walked step by step, so nothing is listed above work it
// waits on, whatever its score.
func TestGroupTaskListPutsWaitingWorkAfterWhatItWaitsOn(t *testing.T) {
	m := kitchenProject(t)
	// The highest priority in the project is on a task that has to wait.
	m.get("fit").SetPriority(todo.PriorityHigh)
	m.markCacheDirty()
	m.ensureCache()
	list := m.groupTaskList(func(x *todo.Todo) bool { return inProjectGroup(x, "Kitchen") })
	pos := make(map[string]int)
	for i, x := range list {
		pos[x.ID] = i
	}
	for _, x := range list {
		for _, dep := range x.Dependencies {
			if p, ok := pos[dep]; ok && p > pos[x.ID] {
				t.Errorf("%q is listed above %q, which it waits on", x.Title, m.get(dep).Title)
			}
		}
	}
}

// The pane numbers the steps, names what each task waits on, and marks the
// rows tied to the selected one: what it waits on, and what waits on it.
func TestGroupPaneShowsTheDependencies(t *testing.T) {
	m := kitchenProject(t)
	m.columns["waits"] = true // off by default
	m.termWidth, m.termHeight = 140, 40
	m = script(t, m, "4")
	m.projectCursor = slices.Index(m.allProjectsForList(), "Kitchen")
	m = script(t, m, "enter")
	for m.currentTodo() == nil || m.currentTodo().ID != "order" {
		m = script(t, m, "down")
	}
	plain := ansi.Strip(m.View())
	// Fit cabinets waits on two tasks; the column clips the second.
	for _, want := range []string{tr("Waits on"), "4 Fit cabinets", "Order cabinets, Tear", "Choose cabinets"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the pane should show %q:\n%s", want, plain)
		}
	}
	// The marked rows. Tests render without colour, so ask the view the row
	// styles are chosen from rather than the escape codes.
	list := m.groupTaskList(func(x *todo.Todo) bool { return inProjectGroup(x, "Kitchen") })
	sel := slices.IndexFunc(list, func(x todo.Todo) bool { return x.ID == "order" })
	cols, _ := m.groupTaskCols(list, false, sel)
	d := cols.deps
	if d == nil {
		t.Fatal("no dependency view for a project whose tasks wait on each other")
	}
	if !d.selWaitsOn["choose"] || len(d.selWaitsOn) != 1 {
		t.Errorf("selected task waits on %v, want only choose", d.selWaitsOn)
	}
	if !d.waitsOnSel["fit"] || len(d.waitsOnSel) != 1 {
		t.Errorf("waiting on the selected task: %v, want only fit", d.waitsOnSel)
	}
	// The tied rows carry their mark before the box, and no other row does.
	for _, line := range strings.Split(plain, "\n") {
		var want string
		switch {
		case strings.Contains(line, "] 2 Choose cabinets") || strings.Contains(line, "]   Choose cabinets"):
			want = "↥["
		case strings.Contains(line, "4 Fit cabinets"):
			want = "↧["
		default:
			if strings.Contains(line, "↥[") || strings.Contains(line, "↧[") {
				t.Errorf("a row not tied to the selected one carries a mark: %q", line)
			}
			continue
		}
		if !strings.Contains(line, want) {
			t.Errorf("row %q should carry %q", line, want)
		}
	}

	// Nothing runs past the pane at any width.
	for _, w := range []int{140, 100, 80, 60, 40} {
		m.termWidth = w
		for _, line := range strings.Split(m.View(), "\n") {
			if ansi.StringWidth(line) > w {
				t.Errorf("width %d: a line is %d wide: %q", w, ansi.StringWidth(line), ansi.Strip(line))
			}
		}
	}
}

// A group where nothing waits on anything keeps the plain list: no step
// numbers and no empty Waits on column.
func TestGroupPaneWithoutDependenciesStaysPlain(t *testing.T) {
	m := modelWithTasks(t, func() todo.Todo { x := todo.New("Paint"); x.Project = "Hall"; return x }())
	m.termWidth, m.termHeight = 140, 40
	m = script(t, m, "4", "enter")
	if plain := ansi.Strip(m.View()); strings.Contains(plain, tr("Waits on")) || strings.Contains(plain, "1 Paint") {
		t.Errorf("a group with no dependencies should draw the plain list:\n%s", plain)
	}
}

// Waits on and Blocks are the two sides of one dependency, on any list: the
// task that waits names what it waits on, and the task it waits on names it.
func TestWaitsOnAndBlocksColumns(t *testing.T) {
	first, second := todo.New("Measure the room"), todo.New("Order the cabinets")
	second.Dependencies = []string{first.ID}
	m := modelWithTasks(t, first, second)
	m.columns["waits"], m.columns["blocks"] = true, true
	m.termWidth, m.termHeight = 160, 20
	row := func(title string) string {
		for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
			// The title column, right after the status box and its arrow.
			if regexp.MustCompile(`\] (↥ |↧ )?` + regexp.QuoteMeta(title)).MatchString(line) {
				return line
			}
		}
		t.Fatalf("no row for %q", title)
		return ""
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, tr("Waits on")) || !strings.Contains(plain, tr("Blocks")) {
		t.Fatalf("both headers should show:\n%s", plain)
	}
	if r := row("Measure the room"); strings.Count(r, "Order the cabinets") != 1 {
		t.Errorf("the task waited on should name what it blocks: %q", r)
	}
	if r := row("Order the cabinets"); strings.Count(r, "Measure the room") != 1 {
		t.Errorf("the waiting task should name what it waits on: %q", r)
	}
}

// The Projects and Tags lists say how much of each group is done.
func TestGroupListsShowTheShareDone(t *testing.T) {
	mk := func(title string, done bool) todo.Todo {
		x := todo.New(title)
		x.Project, x.Tags = "Trip", []string{"trip"}
		if done {
			x.Status = todo.Done
		}
		return x
	}
	m := modelWithTasks(t, mk("book flights", true), mk("pack", true), mk("renew passport", false))
	m.termWidth, m.termHeight = 100, 24
	for _, tab := range []string{"4", "3"} {
		m = script(t, m, tab)
		view := ansi.Strip(m.View())
		if !strings.Contains(view, "Done") || !strings.Contains(view, "67%") {
			t.Errorf("tab %s should show the Done column with 67%%:\n%s", tab, view)
		}
	}
}
