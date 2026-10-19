package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
	"github.com/charmbracelet/x/ansi"
)

// An entry's context follows its title on the same row: nothing for a bare
// entry, otherwise the project before the tags.
func TestTimelineMetaOrder(t *testing.T) {
	if got, w := timelineMeta(dayActivity{title: "bare"}, 80); got != "" || w != 0 {
		t.Errorf("bare entry: want no context, got %q (%d)", got, w)
	}
	if got, _ := timelineMeta(dayActivity{project: "alpha"}, 80); !strings.Contains(got, "[alpha]") {
		t.Errorf("project-only: missing [alpha] in %q", got)
	}
	if got, _ := timelineMeta(dayActivity{tags: []string{"a", "b"}}, 80); !strings.Contains(got, "#a") || !strings.Contains(got, "#b") {
		t.Errorf("tag-only: missing tags in %q", got)
	}
	got, _ := timelineMeta(dayActivity{project: "alpha", tags: []string{"a"}}, 80)
	if p, tg := strings.Index(got, "[alpha]"), strings.Index(got, "#a"); p < 0 || tg < 0 || p > tg {
		t.Errorf("project must precede tags in %q", got)
	}
}

// Short of room, the context drops its tags before its project, clips a
// parent rather than overflow, and reports the width it drew.
func TestTimelineMetaFitsItsWidth(t *testing.T) {
	got, w := timelineMeta(dayActivity{project: "shorty", tags: []string{"a-very-long-tag-name-that-eats-the-width"}}, 12)
	if !strings.Contains(got, "[shorty]") || strings.Contains(got, "a-very-long-tag-name") {
		t.Errorf("want the project kept and the tag dropped at 12 cells: %q", got)
	}
	if sw := ansi.StringWidth(got); sw != w || w > 12 {
		t.Errorf("context is %d cells, reported %d, allowed 12: %q", sw, w, got)
	}
	got, w = timelineMeta(dayActivity{parentTitle: "A parent title that is far too long to fit"}, 12)
	if !strings.HasPrefix(ansi.Strip(got), "↳ A parent") || ansi.StringWidth(got) != w || w > 12 {
		t.Errorf("parent ref should clip to 12 cells: %q (%d)", got, w)
	}
}

// A day's entries take a row each, with the project on the title's row, and
// the time column starts in the same place on every row whatever its kind.
func TestTimelineIsOneRowAnEntry(t *testing.T) {
	m := newTestModel()
	day := localMidnight(-1)
	m.calendar.selected = day
	closed := mkTodo("c", "closed without a timer", todo.Done)
	closed.Project = "home"
	closed.CompletedAt = day.Add(17 * time.Hour)
	timed := mkTodo("t", "timed work", todo.Pending)
	timed.Project = "work"
	timed.Tags = []string{"deep"}
	timed.TimeEntries = []todo.TimeEntry{{ID: "e", StartedAt: day.Add(9 * time.Hour), StoppedAt: day.Add(10 * time.Hour)}}
	m.add(closed)
	m.add(timed)
	m.refreshCaches()

	lines := m.renderTimelineLines(100, 20)
	if len(lines) != 2 {
		t.Fatalf("want one row per entry, got %d:\n%s", len(lines), ansi.Strip(strings.Join(lines, "\n")))
	}
	col := -1
	for _, l := range lines {
		plain := ansi.Strip(l)
		if w := ansi.StringWidth(l); w > 100 {
			t.Errorf("row is %d cells, wider than the pane's 100: %q", w, plain)
		}
		at := strings.Index(plain, "09:00")
		if at < 0 {
			at = strings.Index(plain, "✓ done at")
		}
		at = ansi.StringWidth(plain[:at])
		if col >= 0 && at != col {
			t.Errorf("time column starts at %d and at %d:\n%s", col, at, ansi.Strip(strings.Join(lines, "\n")))
		}
		col = at
	}
	if !strings.Contains(ansi.Strip(lines[0]), "Timed work [work]") || !strings.Contains(ansi.Strip(lines[1]), "Closed without a timer [home]") {
		t.Errorf("want each project on its title's row:\n%s", ansi.Strip(strings.Join(lines, "\n")))
	}
}

// A day of closed tasks and no timer: the title says how many, without a
// "0s" of time, and the timeline has no empty duration column.
func TestTimelineWithoutTimeHasNoDuration(t *testing.T) {
	m := newTestModel()
	day := localMidnight(-1)
	m.calendar.selected = day
	for _, id := range []string{"a", "b"} {
		td := mkTodo(id, "closed "+id, todo.Done)
		td.CompletedAt = day.Add(12 * time.Hour)
		m.add(td)
	}
	m.refreshCaches()
	if got := m.timelineSummary(); got != "2 entries" {
		t.Errorf("summary = %q, want %q", got, "2 entries")
	}
	for _, l := range m.renderTimelineLines(80, 10) {
		if plain := ansi.Strip(l); strings.HasSuffix(plain, " ") {
			t.Errorf("row ends in an empty duration column: %q", plain)
		}
	}
}

// The month grid lights a day where tasks were closed with no time logged,
// and the panel under it counts them for the day and the month.
func TestMonthPanelCountsClosedTasks(t *testing.T) {
	m := newTestModel()
	sel := localMidnight(0)
	other := sel.AddDate(0, 0, -1)
	if other.Month() != sel.Month() {
		other = sel.AddDate(0, 0, 1)
	}
	m.calendar.selected = sel
	m.frameTime = sel.Add(23 * time.Hour)
	for i, at := range []time.Time{sel, sel, other} {
		td := mkTodo(fmt.Sprint(i), "closed", todo.Done)
		td.Project = "home"
		td.CompletedAt = at.Add(12 * time.Hour)
		m.add(td)
	}
	m.refreshCaches()

	lines := m.renderMonthCalendarLines(calPanelWidth - 2)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	words := strings.Join(strings.Fields(plain), " ")
	for _, want := range []string{"Day Completed 2 home 2", "Month Completed 3"} {
		if !strings.Contains(words, want) {
			t.Errorf("month panel lacks %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "Tracked") || strings.Contains(plain, "0s") {
		t.Errorf("a month with no time logged shows a time total:\n%s", plain)
	}
	cell := fmt.Sprintf("%2d", other.Day())
	if unlit := normalStyle.Render(cell); strings.Contains(strings.Join(lines, "\n"), unlit) {
		t.Errorf("day %d closed a task and is drawn unlit", other.Day())
	}
}

// localMidnight returns midnight of today shifted by deltaDays, matching how
// due dates are stored (local midnight, date-only).
func localMidnight(deltaDays int) time.Time {
	now := time.Now()
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return d.AddDate(0, 0, deltaDays)
}

// TestActivitiesForDaySurfacesDueTasks asserts an unfinished task with a future
// DueDate shows up as a "due" activity on its due day, and that a completed task
// with the same due date does not (its deadline is moot).
func TestActivitiesForDaySurfacesDueTasks(t *testing.T) {
	m := newTestModel()
	day := localMidnight(5)

	pending := mkTodo("p", "ship release", todo.Pending)
	pending.DueDate = day
	done := mkTodo("d", "already shipped", todo.Done)
	done.DueDate = day
	m.add(pending)
	m.add(done)
	m.refreshCaches()

	acts := m.activitiesForDay(day)
	if len(acts) != 1 {
		t.Fatalf("activitiesForDay = %d activities, want 1 (done task must not surface)", len(acts))
	}
	if a := acts[0]; !a.due || a.taskID != "p" {
		t.Fatalf("got %+v, want due event for task p", a)
	}

	line := ansi.Strip(m.renderTimelineEntry(acts[0], 0, 80, timelineColumns(acts, m.frameTime)))
	if !strings.Contains(line, "⧗ due") || !strings.Contains(line, "Ship release") {
		t.Fatalf("timeline entry = %q, want due marker + title", line)
	}
}

// TestRenderTimelineEntryOverdue asserts a due event whose day is already past
// renders as "⧗ overdue" rather than "⧗ due".
func TestRenderTimelineEntryOverdue(t *testing.T) {
	m := newTestModel()
	past := localMidnight(-3)
	a := dayActivity{title: "file taxes", start: past, stop: past, due: true}

	line := ansi.Strip(m.renderTimelineEntry(a, 0, 80, timelineColumns([]dayActivity{a}, m.frameTime)))
	if !strings.Contains(line, "⧗ overdue") {
		t.Fatalf("timeline entry = %q, want overdue marker", line)
	}
}

// TestTimelineTicksTrackedTaskCompletedThatDay asserts a task that was timed
// and then closed on the same day shows a ✓ on its entry. The separate
// "done at" row is suppressed when the day has tracked time, so without this
// the entry kept the timer dot and read as still running.
func TestTimelineTicksTrackedTaskCompletedThatDay(t *testing.T) {
	m := newTestModel()
	day := localMidnight(-1)

	tracked := mkTodo("t", "write report", todo.Done)
	tracked.TimeEntries = []todo.TimeEntry{{ID: "e1", StartedAt: day.Add(9 * time.Hour), StoppedAt: day.Add(10 * time.Hour)}}
	tracked.CompletedAt = day.Add(10 * time.Hour)
	m.add(tracked)
	m.refreshCaches()

	acts := m.activitiesForDay(day)
	if len(acts) != 1 || !acts[0].doneThatDay {
		t.Fatalf("activitiesForDay = %+v, want one entry marked doneThatDay", acts)
	}
	line := ansi.Strip(m.renderTimelineEntry(acts[0], 0, 80, timelineColumns(acts, m.frameTime)))
	if !strings.Contains(line, "✓") || strings.Contains(line, "●") {
		t.Fatalf("timeline entry = %q, want ✓ instead of the timer dot", line)
	}
}

// Under the month grid, the open tasks due in the week from the selected day,
// soonest first, cut to the rows the panel has with a count of the rest.
func TestCalendarComingUp(t *testing.T) {
	today := startOfDay(time.Now())
	due := func(title string, days int) todo.Todo {
		td := todo.New(title)
		td.DueDate = today.AddDate(0, 0, days)
		return td
	}
	finished := due("Finished", 1)
	finished.Status = todo.Done
	m := modelWithTasks(t, due("Third", 5), due("First", 0), due("Second", 2), due("Next month", 30), due("Yesterday", -1), finished)
	m.switchTab(tabCalendar)

	got := ansi.Strip(strings.Join(m.renderComingUpLines(20, 10), "\n"))
	for _, want := range []string{"First", "Second", "Third"} {
		if !strings.Contains(got, want) {
			t.Errorf("coming up lacks %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"Next month", "Yesterday", "Finished"} {
		if strings.Contains(got, not) {
			t.Errorf("coming up lists %q, outside the week or done:\n%s", not, got)
		}
	}
	if strings.Index(got, "First") > strings.Index(got, "Second") || strings.Index(got, "Second") > strings.Index(got, "Third") {
		t.Errorf("not soonest first:\n%s", got)
	}

	short := ansi.Strip(strings.Join(m.renderComingUpLines(20, 2), "\n"))
	if !strings.Contains(short, "First") || !strings.Contains(short, "2 more") || strings.Contains(short, "Third") {
		t.Errorf("two rows should be the first task and a count of the rest:\n%s", short)
	}
	for _, line := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(line); w > 20 {
			t.Errorf("line %q is %d wide, over the panel's 20", line, w)
		}
	}
}

// A subtask due the same day as its parent is the parent's deadline again;
// listing it once per step crowds the week out. A subtask with its own date
// keeps its row.
func TestCalendarComingUpListsAParentsDeadlineOnce(t *testing.T) {
	today := startOfDay(time.Now())
	parent := todo.New("Quarterly report")
	parent.DueDate = today.AddDate(0, 0, 3)
	same := todo.New("Draft text")
	same.ParentID = parent.ID
	same.DueDate = parent.DueDate
	own := todo.New("Collect numbers")
	own.ParentID = parent.ID
	own.DueDate = today.AddDate(0, 0, 1)
	m := modelWithTasks(t, parent, same, own)
	m.switchTab(tabCalendar)

	got := ansi.Strip(strings.Join(m.renderComingUpLines(30, 10), "\n"))
	if !strings.Contains(got, "Quarterly report") || !strings.Contains(got, "Collect numbers") {
		t.Errorf("the parent and the subtask with its own date should be listed:\n%s", got)
	}
	if strings.Contains(got, "Draft text") {
		t.Errorf("a subtask due with its parent should not repeat the deadline:\n%s", got)
	}
}

// Coming up sits in the agenda, where the window's width goes to the names,
// and the month panel stays the grid's width however wide the window.
func TestComingUpIsInTheAgenda(t *testing.T) {
	title := "Send the quarterly report to the whole board"
	task := todo.New(title)
	task.DueDate = startOfDay(time.Now()).AddDate(0, 0, 1)
	m := modelWithTasks(t, task)
	m.termWidth, m.termHeight = 120, 30
	m.switchTab(tabCalendar)
	out := ansi.Strip(m.View())
	if !strings.Contains(out, title) {
		t.Errorf("a 120-column calendar clips %q in Coming up:\n%s", title, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, title); i >= 0 {
			// The agenda is the left pane: the row's first border closes
			// after the title, not before it.
			if b := strings.Index(line[i:], "│"); b < 0 || strings.Count(line[:i], "│") != 1 {
				t.Errorf("Coming up is not in the agenda, the left pane:\n%s", line)
			}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, localizedMonthYear(m.calendar.selected)) {
			start := strings.LastIndex(line, "╭")
			if got := ansi.StringWidth(line[start:]); got != calPanelWidth+2 {
				t.Errorf("month panel is %d cells wide, want the fixed %d:\n%s", got, calPanelWidth+2, line)
			}
		}
	}

	// The narrow calendar has no month panel, and keeps Coming up all the same.
	m.termWidth = calSideBySideMinWidth - 1
	if out := ansi.Strip(m.View()); !strings.Contains(out, "Coming up") {
		t.Errorf("the narrow calendar lost Coming up:\n%s", out)
	}
}

// A day too long for its pane still shows Coming up's heading and first task,
// and the timeline scrolls in what is left.
func TestAgendaKeepsComingUpOnALongDay(t *testing.T) {
	day := localMidnight(0)
	var tasks []todo.Todo
	for i := range 20 {
		x := todo.New(fmt.Sprintf("Tracked %d", i))
		x.Project = "work"
		x.TimeEntries = []todo.TimeEntry{{ID: fmt.Sprintf("e%d", i), StartedAt: day.Add(time.Duration(i) * time.Hour), StoppedAt: day.Add(time.Duration(i)*time.Hour + 30*time.Minute)}}
		tasks = append(tasks, x)
	}
	due := todo.New("Renew passport")
	due.DueDate = day.AddDate(0, 0, 2)
	tasks = append(tasks, due)
	m := modelWithTasks(t, tasks...)
	m.switchTab(tabCalendar)
	m.calendar.selected = day
	lines := m.renderAgendaLines(60, 16)
	got := ansi.Strip(strings.Join(lines, "\n"))
	if len(lines) > 16 {
		t.Errorf("the agenda drew %d rows into 16:\n%s", len(lines), got)
	}
	if !strings.Contains(got, "Coming up") || !strings.Contains(got, "Renew passport") {
		t.Errorf("a long day pushed Coming up out:\n%s", got)
	}
	if !strings.Contains(got, "⋮") {
		t.Errorf("want the timeline clipped with ⋮:\n%s", got)
	}
}

// The calendar is this person's days: on a task of a shared project, Anna's
// time stays off Mark's calendar and his day total, and his own shows; on a
// task of his own, every entry is his.
func TestCalendarShowsOnlyOwnTimeOnSharedTasks(t *testing.T) {
	m := newTestModel()
	day := localMidnight(0)
	entry := func(id, author string, from time.Duration) todo.TimeEntry {
		return todo.TimeEntry{ID: id, StartedAt: day.Add(from), StoppedAt: day.Add(from + time.Hour), Author: author}
	}
	shared := mkTodo("s", "Book the ferry", todo.Pending)
	shared.Project = "Trip"
	shared.TimeEntries = []todo.TimeEntry{entry("anna", "Anna", 9*time.Hour), entry("mark", "Mark", 11*time.Hour)}
	own := mkTodo("o", "Post office", todo.Pending)
	own.TimeEntries = []todo.TimeEntry{entry("laptop", "Mark on the laptop", 13*time.Hour)}
	m.add(shared)
	m.add(own)
	m.shared = sharedConfig{Projects: []sharedProject{{ID: "trip", Name: "Trip"}}}
	m.setTimerScope(m.shared.timerScope("Mark"))
	m.refreshCaches()

	var ids []string
	for _, a := range m.activitiesForDay(day) {
		ids = append(ids, a.entryID)
	}
	if !slices.Equal(ids, []string{"mark", "laptop"}) {
		t.Errorf("the day lists entries %v, want Mark's and his own task's", ids)
	}
	if got := m.trackedPerDay(day, day)[dayKey(day)]; got != 2*time.Hour {
		t.Errorf("the day's total is %v, want 2h", got)
	}
	all := []todo.Todo{shared, own}
	if got := trackedTodayDuration(all, day.Add(20*time.Hour), m.timers); got != 2*time.Hour {
		t.Errorf("tracked today is %v, want 2h", got)
	}
}
