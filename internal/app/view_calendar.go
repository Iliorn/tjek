package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Iliorn/tjek/todo"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── Day activities ────────────────────────────────────────────────────────────

type dayActivity struct {
	taskID  string
	entryID string
	title   string
	project string
	tags    []string
	start   time.Time
	stop    time.Time // zero while the entry is still running
	// completed marks this as a completion event (task was marked done on
	// this day) rather than a time-tracking entry. start==stop==CompletedAt
	// and duration is 0; the timeline renders it as "✓ done at HH:MM".
	completed bool
	// due marks this as a deadline event (the task's DueDate falls on this
	// day and it isn't done). start==stop==DueDate and duration is 0; the
	// timeline renders it as "⧗ due" (orange) or "⧗ overdue" (red) when the
	// due day is already past.
	due bool
	// doneThatDay marks a time entry whose task was completed on the same
	// day. The separate "✓ done at" row is suppressed when the day has
	// tracked time, so the entry carries the tick itself — otherwise a
	// finished task showed only the timer dot, which read as still running.
	doneThatDay bool
	// parentTitle is set when the activity's task is a subtask; the timeline
	// shows it as a "↳ parent" reference because subtask titles are often too
	// terse to identify on their own.
	parentTitle string
}

func (a dayActivity) duration() time.Duration {
	if a.completed {
		return 0
	}
	if a.stop.IsZero() {
		return time.Since(a.start)
	}
	return a.stop.Sub(a.start)
}

// calDay is a local calendar day, comparable and usable as a map key. The
// calendar compares a day against every time entry on every frame, so it is
// three integers rather than a formatted string.
type calDay struct {
	year  int
	month time.Month
	day   int
}

func dayKey(t time.Time) calDay {
	// Times come back from storage in UTC; the calendar grid is laid out
	// in the user's local zone, so take the date in local time too —
	// otherwise an entry started at 01:00 local (= 23:00 UTC yesterday)
	// gets keyed to the wrong day.
	y, mo, d := t.Local().Date()
	return calDay{y, mo, d}
}

// activitiesForDay returns every time entry started on the given day plus
// every completion event (a task marked done on that day) so the calendar
// surfaces work even when the user closed the task without tracking time.
// Result is ordered by timestamp.
func (m model) activitiesForDay(day time.Time) []dayActivity {
	key := dayKey(day)
	// Read by the timeline, its title, the roll-ups and the entry cursor's
	// clamp on every key; each build walks every task and time entry. The
	// slice is shared, so it is read-only.
	if m.cache != nil && m.cache.dayActsFor == key && m.cache.dayActs != nil {
		return m.cache.dayActs
	}
	acts := m.buildActivitiesForDay(key)
	if m.cache != nil {
		m.cache.dayActsFor, m.cache.dayActs = key, acts
	}
	return acts
}

func (m model) buildActivitiesForDay(key calDay) []dayActivity {
	var acts []dayActivity
	for _, t := range m.tasks {
		parentTitle := ""
		if t.ParentID != "" {
			if p := m.get(t.ParentID); p != nil {
				parentTitle = p.Title
			}
		}
		doneToday := t.Status == todo.Done && !t.CompletedAt.IsZero() && dayKey(t.CompletedAt) == key
		// The calendar is this person's days: someone else's time on a
		// shared task is theirs (timerScope).
		who := m.timerOwner(t)
		for _, e := range t.TimeEntries {
			if dayKey(e.StartedAt) != key || !e.StartedBy(who) {
				continue
			}
			acts = append(acts, dayActivity{
				doneThatDay: doneToday && !e.StoppedAt.IsZero(),
				taskID:      t.ID,
				entryID:     e.ID,
				title:       t.Title,
				project:     t.Project,
				tags:        t.Tags,
				start:       e.StartedAt,
				stop:        e.StoppedAt,
				parentTitle: parentTitle,
			})
		}
		// Completion event: task marked done on this day. Only surfaces when
		// the day has no tracked time for that task — otherwise the time
		// entries already cover the work and a second "done at HH:MM" row
		// would just be noise.
		if doneToday {
			hasTracked := false
			for _, e := range t.TimeEntries {
				if dayKey(e.StartedAt) == key && e.StartedBy(who) {
					hasTracked = true
					break
				}
			}
			if !hasTracked {
				acts = append(acts, dayActivity{
					taskID:      t.ID,
					title:       t.Title,
					project:     t.Project,
					tags:        t.Tags,
					start:       t.CompletedAt,
					stop:        t.CompletedAt,
					completed:   true,
					parentTitle: parentTitle,
				})
			}
		}
		// Deadline event: an unfinished task whose DueDate lands on this day.
		// Shown regardless of whether the day also has tracked time — the
		// deadline is different information from the work, and due dates are
		// stored at local midnight so this sorts to the top of the day.
		if !t.DueDate.IsZero() && t.Status != todo.Done && dayKey(t.DueDate) == key {
			acts = append(acts, dayActivity{
				taskID:      t.ID,
				title:       t.Title,
				project:     t.Project,
				tags:        t.Tags,
				start:       t.DueDate,
				stop:        t.DueDate,
				due:         true,
				parentTitle: parentTitle,
			})
		}
	}
	// Total order with deterministic tiebreakers — m.tasks is a map
	// (randomized iteration) and sort.Slice isn't stable, so without
	// these, entries with equal start times would reshuffle on every
	// render and appear to "jump" as the cursor moves.
	sort.Slice(acts, func(i, j int) bool {
		if !acts[i].start.Equal(acts[j].start) {
			return acts[i].start.Before(acts[j].start)
		}
		if acts[i].taskID != acts[j].taskID {
			return acts[i].taskID < acts[j].taskID
		}
		return acts[i].entryID < acts[j].entryID
	})
	if acts == nil {
		acts = []dayActivity{} // non-nil, so an empty day is memoized too
	}
	return acts
}

// trackedPerDay sums tracked time per day for entries started in [from, to].
func (m model) trackedPerDay(from, to time.Time) map[calDay]time.Duration {
	end := to.AddDate(0, 0, 1)
	totals := make(map[calDay]time.Duration)
	for _, t := range m.tasks {
		who := m.timerOwner(t)
		for _, e := range t.TimeEntries {
			if e.StartedAt.Before(from) || !e.StartedAt.Before(end) || !e.StartedBy(who) {
				continue
			}
			totals[dayKey(e.StartedAt)] += e.Duration()
		}
	}
	return totals
}

// dueDaysInRange marks days in [from, to] that have at least one unfinished
// task due, so the month grid can hint upcoming deadlines without the user
// landing on each day.
func (m model) dueDaysInRange(from, to time.Time) map[calDay]bool {
	end := to.AddDate(0, 0, 1)
	days := make(map[calDay]bool)
	for _, t := range m.tasks {
		if t.DueDate.IsZero() || t.Status == todo.Done {
			continue
		}
		if t.DueDate.Before(from) || !t.DueDate.Before(end) {
			continue
		}
		days[dayKey(t.DueDate)] = true
	}
	return days
}

// closedPerDay counts the tasks closed on each day in [from, to].
func (m model) closedPerDay(from, to time.Time) map[calDay]int {
	end := to.AddDate(0, 0, 1)
	counts := make(map[calDay]int)
	for _, t := range m.tasks {
		if t.Deleted || t.Status != todo.Done || t.CompletedAt.IsZero() {
			continue
		}
		if t.CompletedAt.Before(from) || !t.CompletedAt.Before(end) {
			continue
		}
		counts[dayKey(t.CompletedAt)]++
	}
	return counts
}

// ── Calendar tab layout ───────────────────────────────────────────────────────

func (m model) buildCalendarContent(w, outerH int) string {
	innerH := panelContentHeight(outerH)
	// The month grid is a fixed 22-column block, so on a narrow window the two
	// panes would join into lines wider than the terminal. Below the threshold,
	// drop the
	// grid and give the timeline the whole width: ← / → and [ / ] still move
	// the day and month, so nothing becomes unreachable, and the month title
	// moves onto the timeline's border so you can still see where you are.
	if w < calSideBySideMinWidth {
		return m.buildCalendarNarrow(w, innerH)
	}
	calW := calPanelWidth
	tlW := w - calW - 4
	if tlW < minInnerWidth {
		tlW = minInnerWidth
	}

	calLines := m.renderMonthCalendarLines(calW - 2)
	tlLines := m.renderAgendaLines(tlW-2, innerH)

	fitLines := func(lines []string, h, contentW int) []string {
		if len(lines) > h {
			lines = lines[:h]
		}
		for len(lines) < h {
			lines = append(lines, "")
		}
		truncateLines(lines, contentW)
		return lines
	}
	// The month panel is as tall as what it shows — the grid and the day's
	// and month's totals — rather than stretched to the window: the empty
	// rows under it would frame nothing. The agenda keeps the full height,
	// since it scrolls through the day.
	for len(calLines) > 0 && strings.TrimSpace(ansi.Strip(calLines[len(calLines)-1])) == "" {
		calLines = calLines[:len(calLines)-1]
	}
	calLines = fitLines(calLines, min(len(calLines), innerH), calW-2)
	tlLines = fitLines(tlLines, innerH, tlW-2)

	// Accent border on the pane that owns keystrokes — same contract as the
	// Tasks side-by-side layout.
	calStyle, tlStyle := listPanelFocusedStyle, listPanelStyle
	if m.calendar.focusTimeline {
		calStyle, tlStyle = listPanelStyle, listPanelFocusedStyle
	}
	calPanel := calStyle.Width(calW).Render(strings.Join(calLines, "\n"))
	tlPanel := tlStyle.Width(tlW).Render(strings.Join(tlLines, "\n"))
	tlPanel = withBorderTitle(tlPanel, m.calendarTimelineTitle(localizedDayDateAbbrev(m.calendar.selected), tlW), tlW, m.calendar.focusTimeline)
	calPanel = withBorderTitle(calPanel, localizedMonthYear(m.calendar.selected), calW, !m.calendar.focusTimeline)
	return lipgloss.JoinHorizontal(lipgloss.Top, tlPanel, calPanel)
}

// buildCalendarNarrow is the single-pane calendar for windows too small to
// hold the month grid beside the timeline. The timeline takes the full width
// with no floor, so it clips to the window instead of overflowing it.
func (m model) buildCalendarNarrow(w, innerH int) string {
	if w < 0 {
		w = 0 // borders only; a floor here would push the box past the window
	}
	lines := m.renderAgendaLines(w-2, innerH)
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	truncateLines(lines, w-2)

	panel := listPanelFocusedStyle.Width(w).Render(strings.Join(lines, "\n"))
	title := localizedDayDateAbbrev(m.calendar.selected) + " · " + localizedMonthYear(m.calendar.selected)
	return withBorderTitle(panel, m.calendarTimelineTitle(title, w), w, true)
}

// ── Month calendar (right panel) ──────────────────────────────────────────────

func (m model) renderMonthCalendarLines(innerW int) []string {
	sel := m.calendar.selected
	monthStart := time.Date(sel.Year(), sel.Month(), 1, 0, 0, 0, 0, sel.Location())
	monthEnd := monthStart.AddDate(0, 1, -1)
	totals := m.trackedPerDay(monthStart, monthEnd)
	closed := m.closedPerDay(monthStart, monthEnd)
	dueDays := m.dueDaysInRange(monthStart, monthEnd)

	var maxDay, monthTotal time.Duration
	for _, d := range totals {
		monthTotal += d
		if d > maxDay {
			maxDay = d
		}
	}
	var maxClosed, monthClosed int
	for _, n := range closed {
		monthClosed += n
		maxClosed = max(maxClosed, n)
	}
	// A day's heat is its share of the month's busiest day by whichever
	// measure says more: time logged or tasks closed. Most work is closed
	// without a timer running, and a grid lit by time alone would leave
	// those days blank.
	heat := func(k calDay) float64 {
		h := 0.0
		if maxDay > 0 {
			h = float64(totals[k]) / float64(maxDay)
		}
		if maxClosed > 0 {
			h = max(h, float64(closed[k])/float64(maxClosed))
		}
		return h
	}

	today := startOfDay(m.frameTime)

	// The grid spreads across the panel: the gap between its seven columns
	// grows with the width (up to calGridMaxGap, past which the days stop
	// reading as one week), and what is left over centres it.
	gap := min(max((innerW-14)/6, 1), calGridMaxGap)
	indent := strings.Repeat(" ", max((innerW-14-6*gap)/2, 0))
	sep := strings.Repeat(" ", gap)

	var lines []string
	lines = append(lines, indent+dimStyle.Render(strings.Join(strings.Fields(localizedWeekdayHeader()), sep)))

	// Monday-first offset of the 1st, matching the stats heatmap convention.
	day := monthStart.AddDate(0, 0, -((int(monthStart.Weekday()) + 6) % 7))
	for day.Before(monthEnd) || day.Equal(monthEnd) {
		cells := make([]string, 0, 7)
		for dow := 0; dow < 7; dow++ {
			if day.Month() != sel.Month() {
				cells = append(cells, "  ")
			} else {
				cell := fmt.Sprintf("%2d", day.Day())
				h := heat(dayKey(day))
				switch {
				case day.Equal(sel):
					cell = calSelectedDayStyle.Render(cell)
				case h > 0:
					idx := min(int(h*float64(len(calGradient)-1)), len(calGradient)-1)
					cell = calGradient[idx].Bold(true).Render(cell)
				case day.Equal(today):
					cell = calTodayStyle.Render(cell)
				case dueDays[dayKey(day)]:
					// Upcoming/unmet deadline: orange + underline, distinct
					// from today (orange bold) and overdue red.
					cell = depOverdueStyle.Underline(true).Render(cell)
				default:
					cell = normalStyle.Render(cell)
				}
				cells = append(cells, cell)
			}
			day = day.AddDate(0, 0, 1)
		}
		lines = append(lines, indent+strings.Join(cells, sep))
	}

	lines = append(lines, m.renderDayRollupLines(innerW)...)

	lines = append(lines, "", dimStyle.Render(tr("Month")))
	lines = append(lines, calTallyLine(tr("Completed"), checkDoneStyle.Render(fmt.Sprint(monthClosed)), innerW))
	if monthTotal > 0 {
		lines = append(lines, calTallyLine(tr("Tracked"), timerStyle.Render(formatDuration(monthTotal)), innerW))
	}
	return lines
}

// calTallyLine is one row of the month panel's totals: the label indented
// under its heading and the styled value right-aligned to the panel's edge.
func calTallyLine(label, value string, innerW int) string {
	labelW := max(innerW-1-ansi.StringWidth(value)-1, 0)
	return " " + dimStyle.Render(padRight(truncate(label, labelW), labelW)) + " " + value
}

// comingUpDays is how far ahead of the selected day the agenda lists
// deadlines: a week, the span the grid's due marks are read across.
const comingUpDays = 7

// dueWithParent reports whether t is a subtask due the same day as its open
// parent. A subtask takes its parent's deadline unless given its own, so a
// list of deadlines would otherwise print a parent's date once per step; the
// parent's row stands for them, and a subtask with a date of its own still
// gets a row.
func dueWithParent(tasks map[string]*todo.Todo, t *todo.Todo) bool {
	if t.ParentID == "" {
		return false
	}
	p := tasks[t.ParentID]
	return p != nil && !p.Deleted && p.Status != todo.Done && !p.DueDate.IsZero() &&
		startOfDay(p.DueDate).Equal(startOfDay(t.DueDate))
}

// renderComingUpLines lists the open tasks due in the week from the selected
// day, soonest first, under the day's agenda: the names behind the month
// grid's due marks, moving with the cursor so the week ahead can be read from
// any day. At most maxRows task rows, the last saying how many more there are,
// after a blank and the heading.
func (m model) renderComingUpLines(w, maxRows int) []string {
	from := startOfDay(m.calendar.selected)
	until := from.AddDate(0, 0, comingUpDays)
	var due []*todo.Todo
	for _, t := range m.tasks {
		if t.Deleted || t.Status == todo.Done || t.DueDate.IsZero() {
			continue
		}
		if !t.DueDate.Before(from) && t.DueDate.Before(until) && !dueWithParent(m.tasks, t) {
			due = append(due, t)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].DueDate.Equal(due[j].DueDate) {
			return due[i].DueDate.Before(due[j].DueDate)
		}
		if due[i].Priority != due[j].Priority {
			return due[i].Priority > due[j].Priority
		}
		return due[i].ID < due[j].ID
	})

	lines := []string{"", dimStyle.Render(tr("Coming up"))}
	if len(due) == 0 {
		return append(lines, dimStyle.Render(" "+tr("nothing due")))
	}
	shown := due
	if len(due) > maxRows {
		shown = due[:max(maxRows-1, 0)]
	}
	for _, t := range shown {
		day := padRight(localizedWeekdayShort(t.DueDate.Weekday()), 3) + fmt.Sprintf(" %02d  ", t.DueDate.Day())
		style := normalStyle
		if t.IsOverdueAt(m.frameTime) {
			style = overdueStyle
		}
		lines = append(lines, dimStyle.Render(day)+style.Render(truncate(t.Title, w-len([]rune(day)))))
	}
	if more := len(due) - len(shown); more > 0 {
		lines = append(lines, dimStyle.Render(strings.TrimSpace(fmt.Sprintf(tr("  … and %d more"), more))))
	}
	return lines
}

// renderDayRollupLines builds the selected day's totals, shown under the
// month grid: the tasks closed and the time logged, each only when there is
// some, then the projects the closed tasks belong to and the projects and
// tags the time went to. A day with neither shows nothing.
func (m model) renderDayRollupLines(innerW int) []string {
	acts := m.activitiesForDay(m.calendar.selected)

	projTotals := make(map[string]time.Duration)
	tagTotals := make(map[string]time.Duration)
	var dayTotal time.Duration
	for _, a := range acts {
		d := a.duration()
		dayTotal += d
		if a.project != "" {
			projTotals[a.project] += d
		}
		for _, tag := range a.tags {
			tagTotals["#"+tag] += d
		}
	}
	key := dayKey(m.calendar.selected)
	closedIn := make(map[string]int)
	var closed int
	for _, t := range m.tasks {
		if t.Deleted || t.Status != todo.Done || t.CompletedAt.IsZero() || dayKey(t.CompletedAt) != key {
			continue
		}
		closed++
		if t.Project != "" {
			closedIn[t.Project]++
		}
	}
	if closed == 0 && dayTotal == 0 {
		return nil
	}

	type rollup struct {
		name string
		d    time.Duration
	}
	top := func(totals map[string]time.Duration, limit int) []rollup {
		rs := make([]rollup, 0, len(totals))
		for name, d := range totals {
			// A task that is only due or done today lends its tags and
			// project to the day without any time; a "0m" row says nothing.
			if d > 0 {
				rs = append(rs, rollup{name, d})
			}
		}
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].d != rs[j].d {
				return rs[i].d > rs[j].d
			}
			return rs[i].name < rs[j].name
		})
		if len(rs) > limit {
			rs = rs[:limit]
		}
		return rs
	}

	lines := []string{"", dimStyle.Render(tr("Day"))}
	if closed > 0 {
		lines = append(lines, calTallyLine(tr("Completed"), checkDoneStyle.Render(fmt.Sprint(closed)), innerW))
	}
	if dayTotal > 0 {
		lines = append(lines, calTallyLine(tr("Tracked"), timerStyle.Render(formatDuration(dayTotal)), innerW))
	}

	byClosed := make([]string, 0, len(closedIn))
	for name := range closedIn {
		byClosed = append(byClosed, name)
	}
	sort.Slice(byClosed, func(i, j int) bool {
		if closedIn[byClosed[i]] != closedIn[byClosed[j]] {
			return closedIn[byClosed[i]] > closedIn[byClosed[j]]
		}
		return byClosed[i] < byClosed[j]
	})
	nameW := innerW - 8 // 1 indent + 7 for the right-aligned count or duration
	for _, name := range byClosed[:min(len(byClosed), 3)] {
		lines = append(lines, " "+projLabelStyle.Render(padRight(truncate(name, nameW), nameW))+
			checkDoneStyle.Render(fmt.Sprintf("%7d", closedIn[name])))
	}
	for _, r := range top(projTotals, 3) {
		lines = append(lines, " "+projLabelStyle.Render(padRight(truncate(r.name, nameW), nameW))+
			timerStyle.Render(fmt.Sprintf("%7s", formatDurationCompact(r.d))))
	}
	for _, r := range top(tagTotals, 3) {
		lines = append(lines, " "+tagStyle.Render(padRight(truncate(r.name, nameW), nameW))+
			timerStyle.Render(fmt.Sprintf("%7s", formatDurationCompact(r.d))))
	}
	return lines
}

// ── Activity timeline (right panel) ───────────────────────────────────────────

// timelineSummary is the "3 entries · 1h 20m" line for the selected day. It
// rides in the agenda pane's border title (see calendarTimelineTitle) rather
// than inside the pane: as a right-aligned first line plus its blank spacer it
// cost two of the agenda's rows on every render, and the border had the room.
func (m model) timelineSummary() string {
	acts := m.activitiesForDay(m.calendar.selected)
	var total time.Duration
	for _, a := range acts {
		total += a.duration()
	}
	if total == 0 {
		// A day of closed tasks and deadlines has no time to sum; "0s"
		// would read as a timer that ran for nothing.
		return trCount("%d entries", len(acts), len(acts))
	}
	if len(acts) == 1 {
		return tr("1 entry · ") + formatDuration(total)
	}
	return trCount("%d entries · %s", len(acts), len(acts), formatDuration(total))
}

// calendarTimelineTitle appends the day's summary in brackets to the agenda
// pane's border title. withBorderTitle truncates at boxW-4, which would eat the
// date before the summary, so the bracket is dropped whole when it doesn't fit
// — the day is the part you can't navigate without.
func (m model) calendarTimelineTitle(base string, boxW int) string {
	title := base + " [" + m.timelineSummary() + "]"
	if len([]rune(title)) > boxW-4 {
		return base
	}
	return title
}

// renderAgendaLines is the day's pane: its timeline, then Coming up in the
// rows the timeline leaves. A day too long for both keeps Coming up to its
// heading, first task and a count of the rest, and scrolls the timeline in
// what remains; a pane too short even for that shows the timeline alone.
func (m model) renderAgendaLines(innerW, innerH int) []string {
	const minUp = 4 // a blank, the heading, a task, a count of the rest
	lines := m.renderTimelineLines(innerW, innerH)
	free := innerH - len(lines)
	if free < minUp {
		if innerH-minUp < agendaMinTimeline {
			return lines
		}
		free = minUp
		lines = m.renderTimelineLines(innerW, innerH-free)
	}
	// Indented by the timeline's cursor gutter, so the two read as one column.
	for _, l := range m.renderComingUpLines(innerW-2, free-2) {
		if l != "" {
			l = "  " + l
		}
		lines = append(lines, l)
	}
	return lines
}

// agendaMinTimeline is the fewest rows the timeline keeps when Coming up
// shares the pane with it: an entry and a ⋮ either side.
const agendaMinTimeline = 3

func (m model) renderTimelineLines(innerW, innerH int) []string {
	acts := m.activitiesForDay(m.calendar.selected)

	var lines []string
	if len(acts) == 0 {
		lines = append(lines, dimStyle.Render(tr("  No activity on this day.")))
		lines = append(lines, dimStyle.Render(tr("  Press t on a task (tab 1) to start tracking.")))
		return lines
	}

	// One row an entry, so the window is the cursor's row with as many
	// either side as fit, less a row for each "⋮" marking entries cut off.
	cur := min(max(m.calendar.entryCursor, 0), len(acts)-1)
	fit := func(rows int) (int, int) {
		rows = max(rows, 1)
		start := max(min(cur-rows/2, len(acts)-rows), 0)
		return start, min(start+rows, len(acts))
	}
	start, end := fit(innerH)
	if start > 0 || end < len(acts) {
		markers := 0
		if start > 0 {
			markers++
		}
		if end < len(acts) {
			markers++
		}
		start, end = fit(innerH - markers)
	}

	cols := timelineColumns(acts, m.frameTime)
	if start > 0 {
		lines = append(lines, ruleStyle.Render("  ⋮"))
	}
	for i := start; i < end; i++ {
		lines = append(lines, m.renderTimelineEntry(acts[i], i, innerW, cols))
	}
	if end < len(acts) {
		lines = append(lines, ruleStyle.Render("  ⋮"))
	}
	return lines
}

// timelineCols is the width of the timeline's right-hand columns, the same on
// every row of the day so the times and durations line up whatever kind of
// entry each row is. A day with no time logged has no duration column, and its
// width goes to the titles.
type timelineCols struct {
	rangeW, durW int
}

func timelineColumns(acts []dayActivity, now time.Time) timelineCols {
	var c timelineCols
	for _, a := range acts {
		c.rangeW = max(c.rangeW, len([]rune(timelineRange(a, now))))
		if !a.completed && !a.due {
			c.durW = 8
		}
	}
	return c
}

// timelineRange is an entry's time column: the span of a time entry, when a
// task was closed, or that it is due.
func timelineRange(a dayActivity, now time.Time) string {
	switch {
	case a.due && a.start.Before(startOfDay(now)):
		return tr("⧗ overdue")
	case a.due:
		return tr("⧗ due")
	case a.completed:
		return tr("✓ done at ") + a.start.Format("15:04")
	case a.stop.IsZero():
		return a.start.Format("15:04") + "–" + tr(" now ")
	}
	return a.start.Format("15:04") + "–" + a.stop.Format("15:04")
}

// renderTimelineEntry is one entry's row: the title with its parent, project
// and tags after it, then the time and duration columns.
func (m model) renderTimelineEntry(a dayActivity, index, innerW int, cols timelineCols) string {
	focused := m.calendar.focusTimeline && index == m.calendar.entryCursor
	cur := "  "
	if focused {
		cur = cursorMark
	}
	running := a.stop.IsZero() && !a.completed && !a.due

	// The title and what places it share what the right-hand columns leave.
	// The context gives way first, down to three fifths of the width for
	// the title; a title shorter than that leaves the rest to the context.
	// The row's fixed cells: cursor 2, mark 1, and a space either side of
	// the title block, plus the duration and its gap when the day has one.
	fixed := 4 + 1 + cols.rangeW
	if cols.durW > 0 {
		fixed += 2 + cols.durW
	}
	leftW := max(innerW-fixed-1, 8)
	titleLen := len([]rune(cleanText(a.title)))
	titleW := min(titleLen, leftW)
	if metaW := timelineMetaWidth(a); metaW > 0 && titleW+1+metaW > leftW {
		titleW = max(leftW-1-metaW, min(titleLen, leftW*3/5))
	}
	title := truncate(a.title, titleW)
	left := normalStyle.Render(title)
	if focused {
		left = selectedStyle.Render(title)
	}
	used := len([]rune(title))
	if meta, w := timelineMeta(a, leftW-used-1); w > 0 {
		left += " " + meta
		used += 1 + w
	}
	left += strings.Repeat(" ", max(leftW-used, 0))

	row := cur + m.timelineDot(a) + " " + left + " " + dimStyle.Render(padRight(timelineRange(a, m.frameTime), cols.rangeW))
	if cols.durW == 0 {
		return row
	}
	dur := strings.Repeat(" ", cols.durW)
	switch {
	case running:
		dur = calTodayStyle.Render(padRight(formatDuration(a.duration())+" ◉", cols.durW))
	case !a.completed && !a.due:
		dur = timerStyle.Render(padRight(formatDuration(a.duration()), cols.durW))
	}
	return row + "  " + dur
}

// timelineDot is the mark at the head of an entry's row: a tick for work
// closed that day, a diamond for a deadline, a dot for time logged.
func (m model) timelineDot(a dayActivity) string {
	switch {
	case a.completed, a.doneThatDay:
		return checkDoneStyle.Render("✓")
	case a.due && a.start.Before(startOfDay(m.frameTime)):
		return overdueStyle.Render("◆")
	case a.due:
		return depOverdueStyle.Render("◆")
	}
	return timerStyle.Render("●")
}

// timelineMetaWidth is the width of an entry's full context: the "↳ parent"
// of a subtask, its project and its tags.
func timelineMetaWidth(a dayActivity) int {
	w, parts := 0, 0
	if a.parentTitle != "" {
		w += len([]rune(cleanText(a.parentTitle))) + 2
		parts++
	}
	if a.project != "" {
		w += len([]rune(cleanText(a.project))) + 2
		parts++
	}
	for _, tag := range a.tags {
		w += len([]rune(cleanText(tag))) + 4 // "⟨#tag⟩ "
	}
	if len(a.tags) > 0 {
		parts++
	}
	return w + max(parts-1, 0)
}

// timelineMeta renders an entry's context in at most avail cells, and its
// width. The parent comes first, clipped to fit, since a subtask's title is
// often too terse to place on its own; then the project and the tags in the
// width left, tags dropped first. Width is counted on the plain text and the
// parts styled at the end, as clipping a styled string would cut its escapes.
func timelineMeta(a dayActivity, avail int) (string, int) {
	if avail < 4 {
		return "", 0
	}
	parentPlain := ""
	if a.parentTitle != "" {
		parentPlain = truncate("↳ "+a.parentTitle, avail)
	}
	parentW := len([]rune(parentPlain))
	rest := avail - parentW
	if parentW > 0 {
		rest-- // separating space before project/tags
	}

	projPlain := ""
	if a.project != "" {
		projPlain = "[" + cleanText(a.project) + "]"
	}
	projW := len([]rune(projPlain))
	tagsW := 0
	for _, tag := range a.tags {
		tagsW += len([]rune(cleanText(tag))) + 4
	}
	showProj := projW > 0 && projW <= rest
	showTags := len(a.tags) > 0
	if showProj && showTags && projW+1+tagsW > rest {
		showTags = false
	}
	if !showProj && showTags && tagsW > rest {
		showTags = false
	}

	var b strings.Builder
	w := 0
	add := func(styled string, width int) {
		if w > 0 {
			b.WriteString(" ")
			w++
		}
		b.WriteString(styled)
		w += width
	}
	if parentW > 0 {
		add(dimStyle.Render(parentPlain), parentW)
	}
	if showProj {
		add(projLabelStyle.Render(projPlain), projW)
	}
	if showTags {
		// renderTagsPart ends each chip with a space; the last one is padding.
		add(renderTagsPart(a.tags), tagsW)
	}
	return b.String(), w
}
