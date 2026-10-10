package app

import (
	"sort"
	"time"

	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/todo"
)

// ── Caches ────────────────────────────────────────────────────────────────────

// cacheState holds derived views recomputed by refreshCaches whenever the task
// set changes. Structural indexes (subtaskOf, runningTimers) live on the Store
// and are maintained incrementally — they're not rebuilt here.
type cacheState struct {
	dirty       bool
	filterDirty bool
	overdueSet  map[string]bool
	blockedSet  map[string]bool // tasks waiting on an unfinished dependency
	blockerSet  map[string]bool // tasks an unfinished task depends on
	// waiting is the tasks hidden until their start date (waitingSet), nil
	// when nothing waits or hiding is off; waitingTop counts the top-level
	// ones for the status line.
	waiting    map[string]bool
	waitingTop int
	// outside is the tasks the active context does not match (context.go),
	// nil when no context is on.
	outside map[string]bool
	// backlog memoizes the Stats backlog series (statsBacklog). A fresh empty
	// memo each refresh, filled the first time the chart is drawn, so a
	// refresh pays nothing for it unless the chart is on screen.
	backlog *backlogMemo
	// dependents lists, per task, the unfinished tasks that wait on it.
	dependents map[string][]string
	active     []todo.Todo
	done       []todo.Todo
	// tagGroups and projectGroups summarize every tag and project for their
	// tabs (see groups.go); tagNames and projectNames are the same keys,
	// alphabetical, for the pickers and completions.
	tagGroups     map[string]*groupSummary
	projectGroups map[string]*groupSummary
	// groupLists memoizes groupTaskList per group (groupListMemo): a drill-in
	// list is read several times per key — the clamp, the anchor, the pane's
	// sizing and its render — and each build walks and ranks the whole set.
	// Cleared with the other derived data, and when a fold changes.
	groupLists map[string][]todo.Todo
	// dayActs memoizes activitiesForDay for the one day it was last asked
	// about (dayActsFor); nil means not built. Cleared with groupLists.
	dayActs       []dayActivity
	dayActsFor    calDay
	tagNames      []string
	projectNames  []string
	tagLastUsed   map[string]time.Time   // tag → latest ModifiedAt of a task using it
	subProgress   map[string]subProgress // parentID → subtask done/total; see refreshSubtaskProgress
	rankScore     map[string]float64     // taskID → the lift the sequence ranking sorts (and shows) it by; see rank.Lifts
	projLastUsed  map[string]time.Time   // project → latest ModifiedAt of a task in it
	taskTagRender map[string]string
	boardCols     [][]todo.Todo // Board-tab columns derived from active/done; see buildBoardColumns
	// closedToday is the tasks completed since midnight, newest first, for the
	// read-out that fills the list pane's spare rows. It is derived here rather
	// than scanned per frame for the reason the row metrics are: View runs on
	// every keystroke, and the done list is the one that only ever grows.
	closedToday []todo.Todo
	// builtAt is when refreshCaches last ran. The overdue set and the sequence
	// order's start-date partition are true for a calendar day, so the minute
	// tick rebuilds once the day has turned.
	builtAt time.Time

	// Tasks-tab column-sizing metrics for the active list: the widest rendered
	// row content and the widest tag cell. Derived from the active set + overdue
	// set, so cached here rather than rescanned every frame (the scan called
	// subtaskProgress per task and dominated the render).
	activeColContentMax int
	activeColTagsMax    int
	activeColHasDue     bool // true when at least one visible active task has a due date
	activeColProjectMax int  // widest project name rune count in the active list (0 = none)
}

// ── Cache management ──────────────────────────────────────────────────────────

func (m *model) refreshCaches() {
	m.frameTime = time.Now()
	m.cache.builtAt = m.frameTime
	m.cache.groupLists, m.cache.dayActs = nil, nil
	m.cache.backlog = &backlogMemo{}

	all := m.allTodos()

	// Momentum reads recent activity; refresh the snapshot before anything
	// downstream (selectActiveDone, rollups) computes scores from it.
	m.rank.Heat = rank.ComputeHeat(m.frameTime, all)
	// The lift depends on the task set, not on the filter, so it is computed
	// once here: the ranking sorts by it, every row prints it, and the filter
	// path below reuses it instead of walking the task set twice per keystroke.
	m.cache.rankScore = rank.Lifts(all, m.rank.Score)
	// The percentage scale is relative to the current field, so its 100% mark
	// is refreshed in the same step — and after the heat, since the scores it
	// takes the maximum of read momentum from it.
	m.rank.Max = rank.MaxRanked(all, m.cache.rankScore, m.rank.Score, m.frameTime)
	m.repo.SetRanker(m.rank)

	for k := range m.cache.overdueSet {
		delete(m.cache.overdueSet, k)
	}
	for i := range all {
		if all[i].IsOverdue() {
			m.cache.overdueSet[all[i].ID] = true
		}
	}

	m.rebuildDependencySets(all)
	// Before the lists: a #tag filter asks it which tags exist (searchEnv).
	m.refreshUsageRecency(all)

	m.cache.waiting, m.cache.waitingTop = nil, 0
	if m.hideWaiting {
		m.cache.waiting, m.cache.waitingTop = waitingSet(all, m.frameTime)
	}
	m.cache.outside = m.outsideContext(all)

	m.cache.active, m.cache.done = selectActiveDoneRanked(all, m.cache.rankScore, m.frameTime, m.rank.ScoreAt(m.frameTime), m.searchQuery, m.searchEnv(), m.focusFilter, m.taskSort, m.historySort, m.listHidden())

	m.refreshGroups(withoutHidden(withoutHidden(all, m.cache.waiting), m.cache.outside))

	// subtaskOf is maintained incrementally by Store.add / Store.remove, so
	// no rebuild is needed here.

	m.refreshSubtaskProgress(all)

	m.refreshTagRenderCache()
	m.refreshTaskColMetrics()
	m.refreshClosedToday()
	m.cache.boardCols = buildBoardColumns(m.boardCfg, m.cache.active, m.cache.done)

	m.cache.dirty = false
	m.cache.filterDirty = false
}

// searchEnv is what the search reads from the caches: which tags exist
// (tagLastUsed holds every tag any task carries) and which tasks are blocked.
// Both are built before the lists in refreshCaches, and a keystroke's
// refreshFilteredCaches finds them warm.
func (m *model) searchEnv() filterEnv {
	tags, blocked := m.cache.tagLastUsed, m.cache.blockedSet
	return filterEnv{
		tagExists: func(tag string) bool { _, ok := tags[tag]; return ok },
		blocked:   func(id string) bool { return blocked[id] },
	}
}

// listHidden is what the lists leave out: the tasks outside the active
// context, and the waiting set unless the search asks for exactly those.
func (m *model) listHidden() map[string]bool {
	waiting := m.cache.waiting
	if searchShowsWaiting(m.searchQuery) {
		waiting = nil
	}
	switch {
	case len(m.cache.outside) == 0:
		return waiting
	case len(waiting) == 0:
		return m.cache.outside
	}
	both := make(map[string]bool, len(waiting)+len(m.cache.outside))
	for id := range waiting {
		both[id] = true
	}
	for id := range m.cache.outside {
		both[id] = true
	}
	return both
}

// withoutHidden is all less the tasks in hidden, for the summaries that count
// what the lists show. It returns all itself when nothing is hidden.
func withoutHidden(all []*todo.Todo, hidden map[string]bool) []*todo.Todo {
	if len(hidden) == 0 {
		return all
	}
	out := make([]*todo.Todo, 0, len(all))
	for _, t := range all {
		if !hidden[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

// rebuildDependencySets recomputes blockedSet/blockerSet from the full task set.
// The rule itself lives in rank.DependencySets (rank/order.go), so the sequence sort
// and the rendered row agree on what "blocked" means.
func (m *model) rebuildDependencySets(all []*todo.Todo) {
	m.cache.blockedSet, m.cache.blockerSet = rank.DependencySets(all)
	m.cache.dependents = make(map[string][]string, len(m.cache.blockerSet))
	for _, t := range all {
		if t.Status == todo.Done {
			continue
		}
		for _, id := range t.Dependencies {
			m.cache.dependents[id] = append(m.cache.dependents[id], t.ID)
		}
	}
}

// refreshUsageRecency records, per tag and per project, the latest ModifiedAt of
// any task carrying it. Detail-pane tag/project search uses these to surface the
// most-recently-used entries first (see sortByRecency). Computed once per cache
// refresh rather than per frame, mirroring the projectTasks rebuild above.
func (m *model) refreshUsageRecency(all []*todo.Todo) {
	for k := range m.cache.tagLastUsed {
		delete(m.cache.tagLastUsed, k)
	}
	for k := range m.cache.projLastUsed {
		delete(m.cache.projLastUsed, k)
	}
	for i := range all {
		mod := all[i].ModifiedAt
		// Every name is entered, even one whose tasks carry no time: the
		// maps also say which tags and projects exist (refreshGroups,
		// searchEnv).
		if p := all[i].Project; p != "" {
			if cur, ok := m.cache.projLastUsed[p]; !ok || mod.After(cur) {
				m.cache.projLastUsed[p] = mod
			}
		}
		for _, tag := range all[i].Tags {
			if cur, ok := m.cache.tagLastUsed[tag]; !ok || mod.After(cur) {
				m.cache.tagLastUsed[tag] = mod
			}
		}
	}
}

// refreshTaskColMetrics recomputes the Tasks-tab column-sizing metrics for the
// active list: the widest rendered row content (title plus every indicator the
// row appends) and the widest tag cell. These depend only on the active set and
// the task tree, so they're computed once per cache refresh instead of being
// rescanned on every frame — the per-frame scan was O(active) and dominated the
// render because it called subtaskProgress for every task.
// The title width comes from taskRowLabel, the same function the row renders
// with, so the longest row cannot eat into the gap before the Score column.
// refreshClosedToday collects the tasks completed since midnight.
//
// It cannot take the head of cache.done and stop at the first older entry: that
// list carries whichever history sort the user picked, and under historySortAlpha
// it is ordered A→Z — so the scan stopped at the first task alphabetically and
// silently dropped the rest of the day, or all of it. The whole list is scanned
// and the result ordered on its own terms.
func (m *model) refreshClosedToday() {
	m.cache.closedToday = m.cache.closedToday[:0]
	today := startOfDay(m.frameTime)
	done := m.cache.done
	for i := range done {
		if startOfDay(done[i].CompletedAt).Equal(today) {
			m.cache.closedToday = append(m.cache.closedToday, done[i])
		}
	}
	sort.Slice(m.cache.closedToday, func(i, j int) bool {
		a, b := m.cache.closedToday[i], m.cache.closedToday[j]
		if !a.CompletedAt.Equal(b.CompletedAt) {
			return a.CompletedAt.After(b.CompletedAt)
		}
		return a.ID < b.ID // total order, same as every other comparator
	})
}

func (m *model) refreshTaskColMetrics() {
	contentMax, tagsMax, projectMax := 0, 0, 0
	hasDue := false
	active := m.cache.active
	for i := range active {
		// Same function the row draws with, so the width reserved here and the
		// width drawn there cannot drift.
		if w := taskRowLabelWidth(m.taskRowLabel(&active[i])); w > contentMax {
			contentMax = w
		}
		if tw := rowTagsWidth(active[i].Tags); tw > tagsMax {
			tagsMax = tw
		}
		if !active[i].DueDate.IsZero() {
			hasDue = true
		}
		if pw := len([]rune(active[i].Project)); pw > projectMax {
			projectMax = pw
		}
	}
	m.cache.activeColContentMax = contentMax
	m.cache.activeColTagsMax = tagsMax
	m.cache.activeColHasDue = hasDue
	m.cache.activeColProjectMax = projectMax
}

// rankedScore is the score shown beside a task's position: its own, lifted by
// whatever it unblocks or contains, exactly as the sequence sort ranked it.
// Reads the cached lift map, so it is a map lookup per row rather than a walk.
func (m model) rankedScore(t *todo.Todo) float64 {
	return rank.ScoreOf(t, m.cache.rankScore, m.rank.Score)
}

// refreshFilteredCaches rebuilds only the views that depend on the search/focus
// filter: the active/done split and the tag-render cache derived from it. The
// data-derived caches (overdue set, tag stats, sorted tags, per-project task
// lists) are left intact because none of them depend on the filter. This is the
// per-keystroke search path — a full refreshCaches would rescan and re-sort the
// entire task set on every keypress for no reason.
func (m *model) refreshFilteredCaches() {
	all := m.allTodos()
	m.cache.backlog = &backlogMemo{} // its scope follows the search
	m.cache.active, m.cache.done = selectActiveDoneRanked(all, m.cache.rankScore, m.frameTime, m.rank.ScoreAt(m.frameTime), m.searchQuery, m.searchEnv(), m.focusFilter, m.taskSort, m.historySort, m.listHidden())
	m.refreshTagRenderCache()
	m.refreshTaskColMetrics()
	m.refreshClosedToday()
	m.cache.boardCols = buildBoardColumns(m.boardCfg, m.cache.active, m.cache.done)
	m.cache.filterDirty = false
}

// refreshGroups rebuilds the Tags and Projects summaries from visible, the
// tasks the lists show. The names the pickers and completions offer come
// from every task instead (the usage maps, refreshUsageRecency), so a tag
// carried only by waiting tasks, or outside the context, can still be picked.
func (m *model) refreshGroups(visible []*todo.Todo) {
	m.cache.tagGroups = summarizeGroups(visible, tagGroupKeys)
	m.cache.projectGroups = summarizeGroups(visible, projectGroupKeys)
	m.cache.tagNames = sortedKeys(m.cache.tagLastUsed)
	m.cache.projectNames = sortedKeys(m.cache.projLastUsed)
}

// sortedKeys is a usage map's names, alphabetical.
func sortedKeys(used map[string]time.Time) []string {
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *model) refreshTagRenderCache() {
	for k := range m.cache.taskTagRender {
		delete(m.cache.taskTagRender, k)
	}
	// It fills on demand in getRenderedTagsForTask: only the rows on screen
	// need their tags, and rendering every task's up front made each tab
	// switch (which refreshes the filtered lists) pay for thousands.
}

// refreshSubtaskProgress rebuilds the parent → (done, total) counts in a single
// pass over the task set, rather than per row: the row-metrics pass asks every
// active task for it.
func (m *model) refreshSubtaskProgress(all []*todo.Todo) {
	if m.cache.subProgress == nil {
		m.cache.subProgress = make(map[string]subProgress, 16)
	}
	for k := range m.cache.subProgress {
		delete(m.cache.subProgress, k)
	}
	for _, t := range all {
		if t.ParentID == "" {
			continue
		}
		p := m.cache.subProgress[t.ParentID]
		p.total++
		if t.Status == todo.Done {
			p.done++
		}
		m.cache.subProgress[t.ParentID] = p
	}
}

// ── Cache accessors ───────────────────────────────────────────────────────────

func (m *model) ensureCache() {
	switch {
	case m.cache.dirty:
		m.refreshCaches()
	case m.cache.filterDirty:
		m.refreshFilteredCaches()
	}
}

func (m model) activeTodos() []todo.Todo {
	return m.cache.active
}

func (m model) completedTodos() []todo.Todo {
	return m.cache.done
}

// renderRowTags draws a task's Tags cell for a list row, taking the whole-set
// render from the by-ID cache whenever every tag fits — which is the common
// case and the one worth caching, since that string is identical frame to
// frame. Only a row that has to drop tags pays to build its own.
func (m *model) renderRowTags(t *todo.Todo, avail int, selected bool) (string, int) {
	if len(t.Tags) == 0 || avail <= 0 {
		return "", 0
	}
	full := 1 + rowTagsWidth(t.Tags)
	if full > avail {
		return renderTaskTagsClipped(t.Tags, avail, selected)
	}
	// The selected row's cell carries the selection background, which the
	// cache does not hold — it is one row per frame, so it renders its own.
	if selected {
		return renderTaskTagCells(rowTagWords(t.Tags), true), full
	}
	return m.getRenderedTagsForTask(t), full
}

// getRenderedTagsForTask returns a task's whole Tags cell via the by-ID cache,
// so a row drawn again costs a map lookup. It fills on demand, for the rows on
// screen, until refreshTagRenderCache clears it.
func (m *model) getRenderedTagsForTask(t *todo.Todo) string {
	if len(t.Tags) == 0 {
		return ""
	}
	if r, ok := m.cache.taskTagRender[t.ID]; ok {
		return r
	}
	rendered := renderTaskTagCells(rowTagWords(t.Tags), false)
	m.cache.taskTagRender[t.ID] = rendered
	return rendered
}
