package app

import (
	"testing"
	"time"

	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/todo"
)

// makeSub is a tiny constructor used across the subtask-behavior tests so the
// fixtures stay readable. Stamps a deterministic CreatedAt offset so order is
// reproducible.
func makeSub(id, title, parentID string, offset time.Duration) todo.Todo {
	t := todo.New(title)
	t.ID = id
	t.ParentID = parentID
	t.CreatedAt = time.Now().Add(offset)
	return t
}

// ── Badges ───────────────────────────────────────────────────────────────────

func TestSubtaskProgressCountsDirectChildren(t *testing.T) {
	parent := makeSub("p", "parent", "", 0)
	c1 := makeSub("c1", "c1", "p", time.Second)
	c2 := makeSub("c2", "c2", "p", 2*time.Second)
	c2.Status = todo.Done
	grand := makeSub("g", "g", "c1", 3*time.Second) // shouldn't count toward p

	m := modelWithTasks(t, parent, c1, c2, grand)
	done, total := m.subtaskProgress("p")
	if total != 2 {
		t.Errorf("total = %d, want 2 (direct children only)", total)
	}
	if done != 1 {
		t.Errorf("done = %d, want 1", done)
	}
}

// ── Parent due-date extension ────────────────────────────────────────────────

func TestExtendParentDueBumpsAncestorsOnlyForward(t *testing.T) {
	now := time.Now()
	parent := makeSub("p", "parent", "", 0)
	parent.DueDate = now.AddDate(0, 0, 5)
	child := makeSub("c", "child", "p", time.Second)
	child.DueDate = now.AddDate(0, 0, 10) // later than parent

	m := modelWithTasks(t, parent, child)
	bumped := m.extendParentDueIfNeeded("c")
	if len(bumped) != 1 || bumped[0] != "p" {
		t.Fatalf("bumped = %v, want [p]", bumped)
	}
	gotP := m.get("p")
	if !gotP.DueDate.Equal(child.DueDate) {
		t.Errorf("parent due = %v, want %v", gotP.DueDate, child.DueDate)
	}
}

func TestExtendParentDueDoesNotShrink(t *testing.T) {
	now := time.Now()
	parent := makeSub("p", "parent", "", 0)
	parent.DueDate = now.AddDate(0, 0, 10)
	child := makeSub("c", "child", "p", time.Second)
	child.DueDate = now.AddDate(0, 0, 5) // earlier than parent

	m := modelWithTasks(t, parent, child)
	bumped := m.extendParentDueIfNeeded("c")
	if len(bumped) != 0 {
		t.Errorf("bumped = %v, want none (child earlier than parent)", bumped)
	}
}

// ── Parent due-date propagation ──────────────────────────────────────────────

func TestPropagateParentDueToAllDescendants(t *testing.T) {
	now := time.Now()
	parent := makeSub("p", "parent", "", 0)
	parent.DueDate = now.AddDate(0, 0, 14)
	child := makeSub("c", "child", "p", time.Second)
	child.DueDate = now.AddDate(0, 0, 3)
	grandchild := makeSub("g", "grandchild", "c", 2*time.Second)
	sibling := makeSub("s", "sibling", "p", 3*time.Second)
	sibling.DueDate = parent.DueDate // already correct; should not be reported
	unrelated := makeSub("u", "unrelated", "", 4*time.Second)
	unrelated.DueDate = now.AddDate(0, 0, 30)

	m := modelWithTasks(t, parent, child, grandchild, sibling, unrelated)
	changed := m.propagateDueToSubtasks("p")
	if got, want := changed, []string{"c", "g"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("changed = %v, want %v", got, want)
	}
	for _, id := range []string{"c", "g", "s"} {
		if got := m.get(id).DueDate; !got.Equal(parent.DueDate) {
			t.Errorf("%s due = %v, want parent due %v", id, got, parent.DueDate)
		}
	}
	if got := m.get("u").DueDate; !got.Equal(unrelated.DueDate) {
		t.Errorf("unrelated due changed to %v, want %v", got, unrelated.DueDate)
	}
}

func TestClearingParentDueClearsDescendants(t *testing.T) {
	due := time.Now().AddDate(0, 0, 7)
	parent := makeSub("p", "parent", "", 0)
	child := makeSub("c", "child", "p", time.Second)
	grandchild := makeSub("g", "grandchild", "c", 2*time.Second)
	parent.DueDate, child.DueDate, grandchild.DueDate = due, due, due

	m := modelWithTasks(t, parent, child, grandchild)
	m.get("p").SetDueDate(time.Time{})
	changed := m.propagateDueToSubtasks("p")
	if len(changed) != 2 {
		t.Fatalf("changed = %v, want both descendants", changed)
	}
	if !m.get("c").DueDate.IsZero() || !m.get("g").DueDate.IsZero() {
		t.Errorf("clearing parent should clear descendants: child=%v grandchild=%v",
			m.get("c").DueDate, m.get("g").DueDate)
	}
}

// ── Priority never outranks the parent ──────────────────────────────────────

func TestParentDowngradeTakesTheSubtreeDown(t *testing.T) {
	parent := makeSub("p", "parent", "", 0)
	parent.Priority = todo.PriorityHigh
	child := makeSub("c", "child", "p", time.Second)
	child.Priority = todo.PriorityHigh
	grandchild := makeSub("g", "grandchild", "c", 2*time.Second)
	grandchild.Priority = todo.PriorityMedium // already below the new cap
	unrelated := makeSub("u", "unrelated", "", 3*time.Second)
	unrelated.Priority = todo.PriorityHigh

	m := modelWithTasks(t, parent, child, grandchild, unrelated)
	m.cyclePriority(m.get("p"), 1) // High → Low

	if got := m.get("p").Priority; got != todo.PriorityLow {
		t.Fatalf("parent priority = %v, want low", got)
	}
	if got := m.get("c").Priority; got != todo.PriorityLow {
		t.Errorf("child priority = %v, want low (followed the parent down)", got)
	}
	if got := m.get("g").Priority; got != todo.PriorityLow {
		t.Errorf("grandchild priority = %v, want low (the cap is transitive)", got)
	}
	if got := m.get("u").Priority; got != todo.PriorityHigh {
		t.Errorf("unrelated priority = %v, want high (untouched)", got)
	}
}

func TestSubtaskCannotOutrankItsParent(t *testing.T) {
	parent := makeSub("p", "parent", "", 0)
	parent.Priority = todo.PriorityLow
	child := makeSub("c", "child", "p", time.Second)
	child.Priority = todo.PriorityLow

	m := modelWithTasks(t, parent, child)
	if !m.cyclePriority(m.get("c"), 1) { // Low → Medium, capped back to Low
		t.Fatal("cycling a subtask past its parent should report the cap")
	}
	if got := m.get("c").Priority; got != todo.PriorityLow {
		t.Errorf("child priority = %v, want low (capped at the parent)", got)
	}
	if got := m.get("p").Priority; got != todo.PriorityLow {
		t.Errorf("parent priority = %v, want low — a subtask never lifts its parent", got)
	}
}

func TestPriorityCycleIsUncappedBelowTheParent(t *testing.T) {
	parent := makeSub("p", "parent", "", 0)
	parent.Priority = todo.PriorityHigh
	child := makeSub("c", "child", "p", time.Second)
	child.Priority = todo.PriorityLow

	m := modelWithTasks(t, parent, child)
	if m.cyclePriority(m.get("c"), 1) { // Low → Medium, under the cap
		t.Error("a step that stays below the parent should not report a cap")
	}
	if got := m.get("c").Priority; got != todo.PriorityMedium {
		t.Errorf("child priority = %v, want medium", got)
	}
}

func TestAnOrphanedSubtaskHasNoCap(t *testing.T) {
	// The parent is gone (tombstoned or never loaded): nothing to clamp
	// against, so the user keeps full control of the task that is left.
	orphan := makeSub("o", "orphan", "missing", 0)
	orphan.Priority = todo.PriorityMedium

	m := modelWithTasks(t, orphan)
	if m.cyclePriority(m.get("o"), 1) {
		t.Error("orphan reported a cap it has no parent for")
	}
	if got := m.get("o").Priority; got != todo.PriorityHigh {
		t.Errorf("orphan priority = %v, want high", got)
	}
}

func TestNewSubtaskInheritsTheParentCap(t *testing.T) {
	parent := todo.New("parent")
	parent.ID = "p"
	parent.Priority = todo.PriorityLow

	sub := todo.NewSubtask("child", parent.ID) // defaults to medium
	sub.InheritContextFrom(&parent, true)
	if got := sub.Priority; got != todo.PriorityLow {
		t.Errorf("new subtask priority = %v, want low (capped at the parent)", got)
	}
}

// ── Auto-close parent ───────────────────────────────────────────────────────

func TestAutoCloseOffDoesNothing(t *testing.T) {
	parent := makeSub("p", "parent", "", 0)
	c := makeSub("c", "c", "p", time.Second)
	c.Status = todo.Done

	m := modelWithTasks(t, parent, c)
	m.autoCloseParent = false
	closed := m.autoCloseAncestorsIfAllDone("c")
	if len(closed) != 0 {
		t.Errorf("setting off, closed = %v, want none", closed)
	}
	if m.get("p").Status != todo.Pending {
		t.Error("parent should remain Pending when auto-close is off")
	}
}

func TestAutoCloseOnClosesParentAndAncestors(t *testing.T) {
	gp := makeSub("gp", "grand", "", 0)
	p := makeSub("p", "parent", "gp", time.Second)
	c := makeSub("c", "child", "p", 2*time.Second)
	c.Status = todo.Done

	m := modelWithTasks(t, gp, p, c)
	m.autoCloseParent = true
	closed := m.autoCloseAncestorsIfAllDone("c")
	if len(closed) != 2 {
		t.Fatalf("closed = %v, want both p and gp", closed)
	}
	if m.get("p").Status != todo.Done {
		t.Error("parent should be auto-closed")
	}
	if m.get("gp").Status != todo.Done {
		t.Error("grandparent should be auto-closed (cascades up)")
	}
}

func TestAutoCloseStopsAtAncestorWithOpenSibling(t *testing.T) {
	gp := makeSub("gp", "grand", "", 0)
	p := makeSub("p", "parent", "gp", time.Second)
	c := makeSub("c", "child", "p", 2*time.Second)
	c.Status = todo.Done
	sibling := makeSub("s", "open sibling under gp", "gp", 3*time.Second) // still open

	m := modelWithTasks(t, gp, p, c, sibling)
	m.autoCloseParent = true
	closed := m.autoCloseAncestorsIfAllDone("c")
	if len(closed) != 1 || closed[0] != "p" {
		t.Fatalf("closed = %v, want [p] only (gp has an open sibling)", closed)
	}
	if m.get("gp").Status != todo.Pending {
		t.Error("grandparent should stay Pending because of the open sibling")
	}
}

// ── Recurring parent: clone subtree reset ────────────────────────────────────

func TestSpawnRecurrenceClonesSubtreeReset(t *testing.T) {
	now := time.Now()
	parent := makeSub("p", "weekly review", "", 0)
	parent.Recurrence = "weekly"
	parent.DueDate = now
	c1 := makeSub("c1", "follow up", "p", time.Second)
	c1.Status = todo.Done
	c1.TimeEntries = []todo.TimeEntry{{StartedAt: now, StoppedAt: now.Add(time.Hour)}}
	c2 := makeSub("c2", "send report", "p", 2*time.Second)
	grand := makeSub("g", "verify sent", "c1", 3*time.Second)
	grand.Status = todo.Done

	m := modelWithTasks(t, parent, c1, c2, grand)
	spawned := m.spawnNextRecurrence(m.get("p"))
	if len(spawned) != 4 {
		t.Fatalf("spawnNextRecurrence returned %v, want the instance and the three clones, to be saved", spawned)
	}
	newID := spawned[0]

	cloneChildren := m.subtaskIDs(newID)
	if len(cloneChildren) != 2 {
		t.Fatalf("new parent has %d children, want 2", len(cloneChildren))
	}
	// Every cloned descendant must be Pending and history-free, regardless of
	// the source's status. That's the contract from the user — recurring task
	// respawns with all children un-done.
	all := append([]string{}, cloneChildren...)
	for i := 0; i < len(all); i++ {
		all = append(all, m.subtaskIDs(all[i])...)
	}
	if len(all) != 3 {
		t.Errorf("cloned descendants = %d, want 3 (c1+c2+grand-equivalents)", len(all))
	}
	for _, id := range all {
		c := m.get(id)
		if c == nil {
			t.Fatalf("clone %s missing", id)
		}
		if c.Status != todo.Pending {
			t.Errorf("clone %s status = %v, want Pending", c.Title, c.Status)
		}
		if len(c.TimeEntries) != 0 {
			t.Errorf("clone %s carried %d time entries, want 0", c.Title, len(c.TimeEntries))
		}
	}
}

// ── Score rollup ─────────────────────────────────────────────────────────────

func TestDescendantScoreRollupLiftsChildScore(t *testing.T) {
	parent := makeSub("p", "parent", "", 0)
	parent.Priority = todo.PriorityLow
	child := makeSub("c", "child", "p", time.Second)
	child.Priority = todo.PriorityHigh
	child.DueDate = time.Now().Add(-24 * time.Hour) // overdue + high priority

	rollup := rank.DescendantRollup(todoPtrs([]todo.Todo{parent, child}), rank.Default().Score)
	if rollup["p"] == 0 {
		t.Error("rollup should report a non-zero score for parent (child is overdue+high)")
	}
	parentOwn := rank.Default().Score(&parent)
	if rollup["p"] <= parentOwn {
		t.Errorf("rollup score %.2f should exceed parent's own %.2f", rollup["p"], parentOwn)
	}
}

// subtaskProgress is served from a cache the refresh builds in one pass, with a
// live walk as the cold path. The two must agree — and a task with no subtasks
// must read as 0/0 from both, which is the case that made the cache useless
// when a missing key was treated as "cache cold".
func TestSubtaskProgressCacheMatchesLiveWalk(t *testing.T) {
	parent := todo.New("Parent")
	first := todo.NewSubtask("One", parent.ID)
	second := todo.NewSubtask("Two", parent.ID)
	second.Status = todo.Done
	lonely := todo.New("No children")

	warm := modelWithTasks(t, parent, first, second, lonely)
	warm.ensureCache()
	if warm.cache.subProgress == nil {
		t.Fatal("the refresh did not build the subtask-progress cache")
	}

	cold := modelWithTasks(t, parent, first, second, lonely)
	cold.cache.subProgress = nil // force the live walk

	for _, id := range []string{parent.ID, lonely.ID, first.ID} {
		wd, wt := warm.subtaskProgress(id)
		cd, ct := cold.subtaskProgress(id)
		if wd != cd || wt != ct {
			t.Errorf("task %.6s: cached %d/%d, live walk %d/%d", id, wd, wt, cd, ct)
		}
	}
	if d, total := warm.subtaskProgress(parent.ID); d != 1 || total != 2 {
		t.Errorf("parent progress = %d/%d, want 1/2", d, total)
	}
	if d, total := warm.subtaskProgress(lonely.ID); d != 0 || total != 0 {
		t.Errorf("childless task = %d/%d, want 0/0", d, total)
	}

	// A subtask closed after the last refresh must show up once the caches are
	// refreshed, the same as every other derived view.
	if sub := warm.get(first.ID); sub != nil {
		sub.Status = todo.Done
	}
	warm.markCacheDirty()
	warm.ensureCache()
	if d, total := warm.subtaskProgress(parent.ID); d != 2 || total != 2 {
		t.Errorf("after closing a subtask: %d/%d, want 2/2", d, total)
	}
}

// Two devices that close the same recurring task before they sync each spawn
// its next instance. The instance and its checklist carry IDs derived from
// the source, so the two spawns are the same tasks and the merge folds them
// into one rather than listing the next "weekly review" twice.
// A subtask due a week before its parent, across the end of summer time:
// midnight to midnight is then a week and an hour, and shifting by that span
// put the next child's due date at 23:00 the day before.
func TestSpawnRecurrenceShiftsDatesByDaysAcrossDST(t *testing.T) {
	cph, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	day := func(m time.Month, d, h int) time.Time { return time.Date(2027, m, d, h, 0, 0, 0, cph) }
	parent := makeSub("p", "weekly review", "", 0)
	parent.Recurrence = "weekly"
	parent.DueDate = day(time.November, 4, 0)
	parent.StartDate = day(time.October, 28, 9) // summer time; the due date is not
	child := makeSub("c", "prep", "p", time.Second)
	child.DueDate = day(time.October, 28, 0)
	child.StartDate = day(time.October, 27, 9)

	m := modelWithTasks(t, parent, child)
	spawned := m.spawnNextRecurrence(m.get("p"))
	if len(spawned) != 2 {
		t.Fatalf("spawned %v, want the instance and its subtask", spawned)
	}
	next, clone := m.get(spawned[0]), m.get(spawned[1])
	for _, c := range []struct {
		what      string
		got, want time.Time
	}{
		{"instance due", next.DueDate, day(time.November, 11, 0)},
		{"instance start", next.StartDate, day(time.November, 4, 9)},
		{"subtask due", clone.DueDate, day(time.November, 4, 0)},
		{"subtask start", clone.StartDate, day(time.November, 3, 9)},
	} {
		if !c.got.Equal(c.want) {
			t.Errorf("%s = %s, want %s", c.what, c.got.In(cph).Format("2006-01-02 15:04"), c.want.Format("2006-01-02 15:04"))
		}
	}
}

func TestRecurrenceSpawnsTheSameTasksOnEveryDevice(t *testing.T) {
	now := time.Now()
	parent := makeSub("p", "weekly review", "", 0)
	parent.Recurrence = "weekly"
	parent.DueDate = now
	child := makeSub("c1", "follow up", "p", time.Second)
	grand := makeSub("g", "verify sent", "c1", 2*time.Second)

	spawnOn := func() []string {
		m := modelWithTasks(t, parent, child, grand)
		ids := m.spawnNextRecurrence(m.get("p"))[:1]
		for i := 0; i < len(ids); i++ {
			ids = append(ids, m.subtaskIDs(ids[i])...)
		}
		return ids
	}
	laptop, desktop := spawnOn(), spawnOn()
	if len(laptop) != 3 {
		t.Fatalf("spawned %d tasks, want the instance and two checklist items", len(laptop))
	}
	for i := range laptop {
		if laptop[i] != desktop[i] {
			t.Errorf("spawn %d: laptop %s, desktop %s; want one ID", i, laptop[i], desktop[i])
		}
	}
}

// Closing a recurring task, reopening it and closing it again spawns its next
// instance once: the second close finds the one the first made.
func TestRecloseDoesNotSpawnASecondInstance(t *testing.T) {
	task := todo.New("water the plants")
	task.Recurrence = "weekly"
	m := modelWithTasks(t, task)

	m.closePendingTask(m.get(task.ID))
	m.pendingReopenID = task.ID
	m.confirmReopen()
	m.closePendingTask(m.get(task.ID))

	instances := 0
	for _, x := range m.tasks {
		if x.Title == task.Title && x.Status == todo.Pending {
			instances++
		}
	}
	if instances != 1 {
		t.Errorf("%d pending next instances, want 1", instances)
	}
}

// A close that is undone removes the instance it spawned, leaving a
// tombstone. Closing again brings the instance back under the same ID, and
// it has to outrank that tombstone or the next save or sync deletes it.
func TestRespawnAfterUndoOutranksTheTombstone(t *testing.T) {
	task := todo.New("pay rent")
	task.Recurrence = "monthly"
	m := modelWithTasks(t, task)

	m = sendKey(t, m, "d")
	nextID := nextInstanceID(task.ID)
	if m.get(nextID) == nil {
		t.Fatal("close did not spawn the next instance")
	}
	m = sendKey(t, m, "u")
	if m.get(nextID) != nil {
		t.Fatal("undo left the spawned instance in place")
	}
	deletedAt, tombstoned := m.tombstones[nextID]
	if !tombstoned {
		t.Fatal("undo of a spawn should tombstone the instance")
	}

	m.closePendingTask(m.get(task.ID))
	back := m.get(nextID)
	if back == nil {
		t.Fatal("closing again did not bring the instance back")
	}
	if _, still := m.tombstones[nextID]; still {
		t.Error("the respawned instance is still tombstoned, so the next save deletes it")
	}
	if !back.ModifiedAt.After(deletedAt) {
		t.Errorf("respawn stamped %v, not after its deletion at %v", back.ModifiedAt, deletedAt)
	}
}
