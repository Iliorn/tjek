package app

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/todo"
)

// ── Tags and Projects as one kind of thing ───────────────────────────────────
//
// A tag and a project are both a named group of tasks, and the two tabs answer
// the same questions about one: how much is still open, when it last moved,
// and what to do next in it. The summary, the ordering, the task list behind a
// row and the rule for hiding finished groups are therefore shared, and each
// tab only says how a task maps onto its groups (tagGroupKeys,
// projectGroupKeys).
//
// Finished work is kept out of the way rather than out of reach: a group with
// nothing open is hidden until h asks for it, and inside a group the done tasks
// fold into one line under the open ones. Most of what a long-lived store
// holds is finished, and giving it equal space buried the few rows still in
// play.

// groupSort is the order the Tags and Projects lists are drawn in.
type groupSort int

const (
	// groupSortOpen puts the groups with the most open work first — the
	// default, since that is what the tabs are for.
	groupSortOpen groupSort = iota
	groupSortRecent
	groupSortName
	groupSortCount
)

func (s groupSort) next() groupSort { return (s + 1) % groupSortCount }

func (s groupSort) label() string {
	switch s {
	case groupSortRecent:
		return tr("recent")
	case groupSortName:
		return tr("alpha")
	default:
		return tr("open")
	}
}

// groupSummary is everything a Tags or Projects row says about its group.
type groupSummary struct {
	open, overdue, done int
	// last is the newest ModifiedAt among the group's tasks: an edit, a
	// completion or a time entry all move it.
	last time.Time
}

func (s *groupSummary) finished() bool { return s.open == 0 }

// tagGroupKeys visits the tag groups t belongs to. A top-level task without
// tags belongs to the virtual (untagged) row; an untagged subtask belongs to
// its parent, so it is nobody's triage problem and counts nowhere.
func tagGroupKeys(t *todo.Todo, visit func(string)) {
	if len(t.Tags) == 0 {
		if t.ParentID == "" {
			visit(untaggedKey)
		}
		return
	}
	for _, tag := range t.Tags {
		visit(tag)
	}
}

func projectGroupKeys(t *todo.Todo, visit func(string)) {
	if t.Project != "" {
		visit(t.Project)
	}
}

// inTagGroup and inProjectGroup are the membership tests behind a drilled-in
// list, and must agree with the key functions above or a row's counts would
// describe a list other than the one enter opens.
func inTagGroup(t *todo.Todo, key string) bool {
	if key == untaggedKey {
		return len(t.Tags) == 0 && t.ParentID == ""
	}
	for _, tag := range t.Tags {
		if tag == key {
			return true
		}
	}
	return false
}

func inProjectGroup(t *todo.Todo, key string) bool { return t.Project == key }

// summarizeGroups builds one summary per group in a single pass over the task
// set.
func summarizeGroups(all []*todo.Todo, keys func(*todo.Todo, func(string))) map[string]*groupSummary {
	out := make(map[string]*groupSummary)
	for _, t := range all {
		keys(t, func(key string) {
			g := out[key]
			if g == nil {
				g = &groupSummary{}
				out[key] = g
			}
			if t.ModifiedAt.After(g.last) {
				g.last = t.ModifiedAt
			}
			if t.Status == todo.Done {
				g.done++
				return
			}
			g.open++
			if t.IsOverdue() {
				g.overdue++
			}
		})
	}
	return out
}

// sortGroupKeys orders group names for display. Every mode ends at the name so
// the list never reshuffles between two frames over the same data.
func sortGroupKeys(keys []string, mode groupSort, sums map[string]*groupSummary) {
	byName := func(a, b string) bool {
		if la, lb := strings.ToLower(a), strings.ToLower(b); la != lb {
			return la < lb
		}
		return a < b
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		sa, sb := sums[a], sums[b]
		if sa == nil || sb == nil {
			return byName(a, b)
		}
		switch mode {
		case groupSortOpen:
			if sa.open != sb.open {
				return sa.open > sb.open
			}
			if !sa.last.Equal(sb.last) {
				return sa.last.After(sb.last)
			}
		case groupSortRecent:
			if !sa.last.Equal(sb.last) {
				return sa.last.After(sb.last)
			}
		}
		return byName(a, b)
	})
}

// visibleGroups is the list a Tags or Projects tab draws: the groups matching
// the tab's filter, sorted, with the finished ones left out unless shown.
// pinned stays visible regardless — it is the group the cursor is inside, and
// finishing its last open task must not pull the list out from under you.
// The (untagged) row sorts like any other group, so the list reads in the
// order its title names; by name its key sorts ahead of every tag.
func visibleGroups(sums map[string]*groupSummary, mode groupSort, showFinished bool, pinned string, match func(string) bool) []string {
	keys := make([]string, 0, len(sums))
	for key, s := range sums {
		if !showFinished && s.finished() && key != pinned {
			continue
		}
		if !match(key) {
			continue
		}
		keys = append(keys, key)
	}
	sortGroupKeys(keys, mode, sums)
	return keys
}

// hiddenFinishedGroups counts the finished groups visibleGroups is leaving out.
func hiddenFinishedGroups(sums map[string]*groupSummary, showFinished bool, match func(string) bool) int {
	if showFinished {
		return 0
	}
	n := 0
	for key, s := range sums {
		if s.finished() && match(key) {
			n++
		}
	}
	return n
}

// groupTaskList is the task list behind a group's row, in the order the
// drilled-in list walks it: open tasks first, by step (groupSteps) and within
// a step in the Tasks tab's sequence order, so what can start today leads;
// then, only when finished work is shown, the done tasks newest first. Subtasks fold the
// way they do on the Tasks tab, sharing its expandedTasks: an unfolded
// parent is followed by all of its subtasks, whichever group those carry, and
// a folded one hides them. A subtask whose parent is not open in the group
// stands as a row of its own.
func (m model) groupTaskList(match func(*todo.Todo) bool) []todo.Todo {
	var open, done []*todo.Todo
	inOpen := make(map[string]bool)
	for _, t := range m.tasks {
		if !match(t) || m.cache.waiting[t.ID] {
			continue
		}
		if t.Status == todo.Done {
			done = append(done, t)
			continue
		}
		open = append(open, t)
		inOpen[t.ID] = true
	}
	roots := open[:0:0]
	for _, t := range open {
		if t.ParentID == "" || !inOpen[t.ParentID] {
			roots = append(roots, t)
		}
	}
	rank.SortPtrs(roots, m.cache.rankScore, rank.Sunk(m.cache.blockedSet, roots, m.frameTime), m.rank.ScoreNow())
	steps := groupSteps(roots, m.get)
	sort.SliceStable(roots, func(i, j int) bool { return steps[roots[i].ID] < steps[roots[j].ID] })

	out := make([]todo.Todo, 0, len(open)+len(done))
	listed := make(map[string]bool)
	for _, t := range roots {
		out = append(out, *t)
		if !m.expandedTasks[t.ID] {
			continue
		}
		for _, id := range m.subtaskIDs(t.ID) {
			if sub := m.get(id); sub != nil {
				out = append(out, *sub)
				listed[id] = true
			}
		}
	}
	if m.showFinishedGroups {
		sort.Slice(done, func(i, j int) bool {
			if !done[i].CompletedAt.Equal(done[j].CompletedAt) {
				return done[i].CompletedAt.After(done[j].CompletedAt)
			}
			return done[i].ID < done[j].ID
		})
		for _, t := range done {
			if !listed[t.ID] {
				out = append(out, *t)
			}
		}
	}
	return out
}

// groupSteps numbers a group's open top-level tasks by when they can be
// done: step 1 is what nothing holds up, and a task waiting on others comes
// one step after the latest of them. Work it waits on outside these tasks (in
// another group, or a subtask) is not in the list to number, so it puts the
// task at step 2 at least. A dependency cycle is cut where the walk meets it.
func groupSteps(tasks []*todo.Todo, get func(string) *todo.Todo) map[string]int {
	in := make(map[string]*todo.Todo, len(tasks))
	for _, t := range tasks {
		in[t.ID] = t
	}
	steps := make(map[string]int, len(tasks))
	var visit func(t *todo.Todo) int
	visit = func(t *todo.Todo) int {
		if s, ok := steps[t.ID]; ok {
			return max(s, 1) // 0: still being walked, so a cycle
		}
		steps[t.ID] = 0
		s := 1
		for _, id := range t.Dependencies {
			if d := in[id]; d != nil {
				s = max(s, visit(d)+1)
			} else if d := get(id); d != nil && d.Status != todo.Done {
				s = max(s, 2)
			}
		}
		steps[t.ID] = s
		return s
	}
	for _, t := range tasks {
		visit(t)
	}
	return steps
}

// groupDeps is a group pane's dependency view: each top-level row's step, the
// first row of each step (the one that shows its number), and the rows tied
// to the selected one.
type groupDeps struct {
	step  map[string]int
	first map[string]bool
	stepW int // the step column, its gap included
	// selWaitsOn is what the selected task waits on; waitsOnSel is what waits
	// on it.
	selWaitsOn, waitsOnSel map[string]bool
}

// groupDepsFor builds the dependency view of a group's rows, or nil when no
// row waits on anything: every task is then step 1. sel is the drill cursor, -1 for a preview.
func (m model) groupDepsFor(tasks []todo.Todo, nested []bool, sel int) *groupDeps {
	var roots []*todo.Todo
	for i := range tasks {
		if !nested[i] && tasks[i].Status != todo.Done {
			roots = append(roots, &tasks[i])
		}
	}
	steps := groupSteps(roots, m.get)
	top := 1
	for _, s := range steps {
		top = max(top, s)
	}
	if top == 1 {
		return nil
	}
	d := &groupDeps{
		step:       steps,
		first:      make(map[string]bool),
		stepW:      runeLen(strconv.Itoa(top)) + 1,
		selWaitsOn: make(map[string]bool),
		waitsOnSel: make(map[string]bool),
	}
	last := 0
	for _, t := range roots {
		if s := steps[t.ID]; s != last {
			d.first[t.ID] = true
			last = s
		}
	}
	if sel >= 0 && sel < len(tasks) {
		cur := &tasks[sel]
		for _, id := range cur.Dependencies {
			d.selWaitsOn[id] = true
		}
		for i := range tasks {
			if slices.Contains(tasks[i].Dependencies, cur.ID) {
				d.waitsOnSel[tasks[i].ID] = true
			}
		}
	}
	return d
}

// groupNestedRows reports, per row of a groupTaskList, whether it is drawn
// indented as a subtask of the unfolded parent above it. An unfolded parent's
// subtasks are listed straight after it and nowhere else, so a row is nested
// exactly when it continues that run.
func (m model) groupNestedRows(tasks []todo.Todo) []bool {
	nested := make([]bool, len(tasks))
	for i := 1; i < len(tasks); i++ {
		p, prev := tasks[i].ParentID, &tasks[i-1]
		nested[i] = p != "" && m.expandedTasks[p] &&
			(prev.ID == p || (nested[i-1] && prev.ParentID == p))
	}
	return nested
}

// hasDatedOpenTask reports whether a timeline has anything ahead of it to
// draw: an open task with a start or a due date. Without one the chart is only
// a record of when things were finished.
func hasDatedOpenTask(tasks []todo.Todo) bool {
	for i := range tasks {
		t := &tasks[i]
		if t.Status != todo.Done && (!plannedStart(*t).IsZero() || !t.DueDate.IsZero()) {
			return true
		}
	}
	return false
}

// formatSince renders how long ago a group last moved, in the fewest cells
// that still read at a glance.
func formatSince(at, now time.Time) string {
	if at.IsZero() {
		return "-"
	}
	days := calendarDays(at, now)
	switch {
	case days <= 0:
		return tr("today")
	case days < 14:
		return strconv.Itoa(days) + "d"
	case days < 70:
		return strconv.Itoa(days/7) + "w"
	default:
		return strconv.Itoa(days/30) + "mo"
	}
}
