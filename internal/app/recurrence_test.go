package app

import (
	"strings"
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
)

func TestParseRecurInput(t *testing.T) {
	now := time.Now()
	inAMonth := startOfDay(now).AddDate(0, 1, 0).Format("2006-01-02")
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{"Weekly", "weekly", true},
		{"thu,mon", "weekly:mon,thu", true},
		{"monday,thursday", "weekly:mon,thu", true},
		{"2w:fri", "every:2w:fri", true},
		{"every:2w:fri", "every:2w:fri", true},
		{"monthly/until:31-12-27", "monthly/until:2027-12-31", true},
		{"monthly/until:+1m", "monthly/until:" + inAMonth, true},
		{"daily/10x", "daily/count:10", true},
		{"monthly/until:someday", "", false},
		{"fortnightly", "", false},
	} {
		got, ok := parseRecurInput(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseRecurInput(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
	withLang(t, langDA, func() {
		for in, want := range map[string]string{
			"ugentligt":                 "weekly",
			"man,tor":                   "weekly:mon,thu",
			"2w:fre":                    "every:2w:fri",
			"ugentligt:man":             "weekly:mon",
			"månedligt/indtil:31-12-27": "monthly/until:2027-12-31",
			"weekly/until:31-12-27":     "weekly/until:2027-12-31", // English still parses
		} {
			if got, ok := parseRecurInput(in); !ok || got != want {
				t.Errorf("da: parseRecurInput(%q) = (%q, %v), want %q", in, got, ok, want)
			}
		}
	})
}

func TestTrRecurrence(t *testing.T) {
	for rule, want := range map[string]string{
		"weekly":                         "weekly",
		"every:3d":                       "every:3d",
		"weekly:mon,thu":                 "weekly mon,thu",
		"every:2w:fri/until:2027-12-31":  "every:2w fri until 31-12-27",
		"daily/count:10":                 "daily 10 times",
		"something a newer tjek invents": "something a newer tjek invents",
	} {
		if got := trRecurrence(rule); got != want {
			t.Errorf("trRecurrence(%q) = %q, want %q", rule, got, want)
		}
	}
	withLang(t, langDA, func() {
		if got, want := trRecurrence("weekly:mon,thu/count:8"), "ugentligt man,tor 8 gange"; got != want {
			t.Errorf("da: trRecurrence = %q, want %q", got, want)
		}
	})
}

// closeAndSpawn closes the instance id and returns the one it spawned.
func closeAndSpawn(t *testing.T, m *model, id string) *todo.Todo {
	t.Helper()
	src := m.get(id)
	src.Toggle()
	ids := m.spawnNextRecurrence(src)
	if len(ids) == 0 {
		return nil
	}
	return m.get(ids[0])
}

// A monthly series on the 31st falls on February's last day, and comes back
// to the 31st after it.
func TestMonthlySeriesComesBackToItsDay(t *testing.T) {
	rent := todo.New("rent")
	rent.SetDueDate(time.Date(2027, 1, 31, 0, 0, 0, 0, time.Local))
	rent.SetRecurrence("monthly")
	m := modelWithTasks(t, rent)
	id := rent.ID
	var got []string
	for range 4 {
		next := closeAndSpawn(t, &m, id)
		if next == nil {
			t.Fatal("no next instance")
		}
		got = append(got, next.DueDate.Format("01-02"))
		id = next.ID
	}
	if want := "02-28 03-31 04-30 05-31"; strings.Join(got, " ") != want {
		t.Errorf("series: %s, want %s", strings.Join(got, " "), want)
	}
}

// Moving one instance's due date moves that instance alone; setting the
// rule again moves the series.
func TestMovingAnInstanceKeepsTheSeries(t *testing.T) {
	mon := time.Date(2027, 3, 1, 0, 0, 0, 0, time.Local) // a Monday
	task := todo.New("weekly review")
	task.SetDueDate(mon)
	task.SetRecurrence("weekly")
	m := modelWithTasks(t, task)

	m.get(task.ID).SetDueDate(mon.AddDate(0, 0, 2)) // put off to Wednesday
	next := closeAndSpawn(t, &m, task.ID)
	if want := mon.AddDate(0, 0, 7); next == nil || !next.DueDate.Equal(want) {
		t.Fatalf("after a moved instance: next = %v, want the Monday after, %v", next, want)
	}

	next.SetDueDate(mon.AddDate(0, 0, 10)) // Thursday...
	next.SetRecurrence("weekly")           // ...and the series with it
	after := closeAndSpawn(t, &m, next.ID)
	if want := mon.AddDate(0, 0, 17); after == nil || !after.DueDate.Equal(want) {
		t.Errorf("after setting the rule again: next = %v, want the Thursday after, %v", after, want)
	}
}

func TestEndedSeriesSpawnsNothing(t *testing.T) {
	task := todo.New("course")
	task.SetDueDate(time.Date(2027, 3, 1, 0, 0, 0, 0, time.Local))
	task.SetRecurrence("weekly/count:2")
	m := modelWithTasks(t, task)
	second := closeAndSpawn(t, &m, task.ID)
	if second == nil {
		t.Fatal("the second of two was not spawned")
	}
	if third := closeAndSpawn(t, &m, second.ID); third != nil {
		t.Errorf("a third instance of two, due %v", third.DueDate)
	}
}

// r on the Recurrence field writes any rule, and the series counts from the
// due date it is written on.
func TestDetailWritesARule(t *testing.T) {
	task := todo.New("gym")
	task.SetDueDate(time.Date(2027, 3, 1, 0, 0, 0, 0, time.Local))
	m := modelWithTasks(t, task)
	m.pane = paneDetail
	m.detailTaskID = task.ID
	m.detail.field = fieldRecurrence

	m = sendKey(t, m, "r")
	if m.mode != modeInput {
		t.Fatalf("r on Recurrence: mode = %v, want modeInput", m.mode)
	}
	m = typeInto(t, m, "mon,thu/until:31-03-27")
	m = sendKey(t, m, "enter")
	got := m.get(task.ID)
	if got.Recurrence != "weekly:mon,thu/until:2027-03-31" || !got.RecurFrom.Equal(task.DueDate) {
		t.Fatalf("rule %q from %v, want weekly:mon,thu/until:2027-03-31 from the due date", got.Recurrence, got.RecurFrom)
	}

	m = sendKey(t, m, "r")
	if v := m.textInput.Value(); v != "weekly:mon,thu/until:31-03-27" {
		t.Errorf("editor opens on %q, want the rule with its date as typed", v)
	}
	m.textInput.SetValue("fortnightly")
	m = sendKey(t, m, "enter")
	if m.mode != modeInput || m.get(task.ID).Recurrence != "weekly:mon,thu/until:2027-03-31" {
		t.Errorf("a rule tjek cannot read: mode %v, rule %q; want the prompt kept and the rule unchanged", m.mode, m.get(task.ID).Recurrence)
	}

	m.performUndo()
	if r := m.get(task.ID).Recurrence; r != "" {
		t.Errorf("undo left the rule %q", r)
	}
}

func TestRecurFromSurvivesTheStore(t *testing.T) {
	h := openTestDB(t)
	a := todo.New("rent")
	a.SetDueDate(time.Date(2027, 1, 31, 0, 0, 0, 0, time.Local))
	a.SetRecurrence("monthly")
	a.SetDueDate(time.Date(2027, 2, 3, 0, 0, 0, 0, time.Local))
	saveTodos(t, h, []todo.Todo{a})
	for name, load := range map[string]func(querier) ([]todo.Todo, error){
		"live": loadTodosFromDB, "sync": loadTodosForSync,
	} {
		got, err := load(h)
		if err != nil || len(got) != 1 {
			t.Fatalf("%s load: %v, %d tasks", name, err, len(got))
		}
		if !got[0].RecurFrom.Equal(a.RecurFrom) {
			t.Errorf("%s load: RecurFrom = %v, want %v", name, got[0].RecurFrom, a.RecurFrom)
		}
	}
}

// tjek edit --recur sets the rule and starts its series from the due date
// set with it.
func TestCliEditRecur(t *testing.T) {
	setTestHome(t, t.TempDir())
	id := strings.TrimSpace(captureStdout(t, func() {
		if code := cliAdd([]string{"rent", "--due", "31-01-27", "--quiet-id"}); code != 0 {
			t.Fatalf("add: exit %d", code)
		}
	}))
	captureStdout(t, func() {
		if code := cliEdit([]string{id, "--recur", "monthly/count:12"}); code != 0 {
			t.Fatalf("edit: exit %d", code)
		}
	})
	_, todos, err := loadForCLI()
	if err != nil || len(todos) != 1 {
		t.Fatalf("load: %v, %d tasks", err, len(todos))
	}
	if got := todos[0]; got.Recurrence != "monthly/count:12" || got.RecurFrom.Format("2006-01-02") != "2027-01-31" {
		t.Errorf("rule %q from %v, want monthly/count:12 from 2027-01-31", got.Recurrence, got.RecurFrom)
	}
	captureStdout(t, func() {
		if code := cliEdit([]string{id, "--clear-recur"}); code != 0 {
			t.Fatalf("edit --clear-recur: exit %d", code)
		}
	})
	if _, todos, _ = loadForCLI(); todos[0].IsRecurring() || !todos[0].RecurFrom.IsZero() {
		t.Errorf("after --clear-recur: rule %q from %v", todos[0].Recurrence, todos[0].RecurFrom)
	}
}
