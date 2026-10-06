package app

import (
	"encoding/binary"
	"hash/maphash"
	"time"

	"github.com/Iliorn/tjek/todo"
)

// Store is the single source of truth: the task set, the undo history, and the
// per-save change set (dirtyIDs / tombstones).
//
// Tasks are held in a map keyed by ID so:
//   - Lookup by ID is O(1) with no separate index.
//   - Pointers into the map are stable for the lifetime of the value, so
//     mutators can hand *todo.Todo around without worrying about slice growth.
//   - Mutation cost is independent of the corpus size.
//
// Order is *derived* — when the UI needs a sorted list it goes through a
// selector against an ordered view, never directly through map iteration.
type Store struct {
	tasks     map[string]*todo.Todo
	undoStack []undoEntry
	dirtyIDs  map[string]struct{}
	// tombstones maps a deleted ID to the moment it was deleted. The time is
	// the deletion's *event* time for merge purposes: an undo restores the
	// task with a ModifiedAt clamped past it (touchRestored), so the restore
	// out-times its own deletion even on a clock too coarse to separate the
	// two keystrokes.
	tombstones map[string]time.Time

	// base is, for each task loaded from or saved to the store, the task as
	// this copy last agreed with the store on: what a save writes the task's
	// edits against (rebase.go). A task with none is saved whole. Entries are
	// never changed in place, only replaced, so a save can read one off the
	// Update goroutine.
	base map[string]*todo.Todo
	// versions is the taskVersion of the stored version each base was set
	// from, and baseFingerprint their XOR: the store as this copy last
	// agreed with it, which a reload compares the loaded set against
	// without hashing memory (sameAsLoaded). Kept by setBase and dropBase.
	versions        map[string]uint64
	baseFingerprint uint64

	// Maintained indexes. Update them via Store mutators; never write directly
	// or they will drift from `tasks`.
	subtaskOf     map[string][]string // parentID → child IDs in CreatedAt order
	runningTimers map[string]struct{} // task IDs with a running entry of this device's (timers)

	// timers says which running entries are this device's to run; set with
	// setTimerScope.
	timers timerScope
}

// setTimerScope sets whose timers the store runs and rebuilds runningTimers
// to match.
func (s *Store) setTimerScope(sc timerScope) {
	s.timers = sc
	s.ensureTasks()
	clear(s.runningTimers)
	for id, t := range s.tasks {
		if t.TimerRunningBy(sc.owner(t)) {
			s.runningTimers[id] = struct{}{}
		}
	}
}

// timerOwner is the who of t's timer methods on this device.
func (s *Store) timerOwner(t *todo.Todo) string { return s.timers.owner(t) }

func (s *Store) ensureTasks() {
	if s.tasks == nil {
		s.tasks = make(map[string]*todo.Todo)
	}
	if s.subtaskOf == nil {
		s.subtaskOf = make(map[string][]string)
	}
	if s.runningTimers == nil {
		s.runningTimers = make(map[string]struct{})
	}
}

// addSubtaskOf inserts childID into subtaskOf[parentID] at the position that
// keeps the slice in CreatedAt order. O(siblings) per call — fast in practice
// since subtask counts are tiny.
func (s *Store) addSubtaskOf(parentID string, child *todo.Todo) {
	if parentID == "" || child == nil {
		return
	}
	siblings := s.subtaskOf[parentID]
	insertAt := len(siblings)
	for i, sibID := range siblings {
		sib := s.tasks[sibID]
		if sib == nil {
			continue
		}
		if child.CreatedAt.Before(sib.CreatedAt) {
			insertAt = i
			break
		}
	}
	siblings = append(siblings, "")
	copy(siblings[insertAt+1:], siblings[insertAt:])
	siblings[insertAt] = child.ID
	s.subtaskOf[parentID] = siblings
}

func (s *Store) removeSubtaskOf(parentID, childID string) {
	if parentID == "" {
		return
	}
	siblings := s.subtaskOf[parentID]
	for i, id := range siblings {
		if id == childID {
			s.subtaskOf[parentID] = append(siblings[:i], siblings[i+1:]...)
			break
		}
	}
	if len(s.subtaskOf[parentID]) == 0 {
		delete(s.subtaskOf, parentID)
	}
}

// startTimer / stopTimer wrap todo.Todo's timer mutators, on this device's
// own timers (timers), and keep the runningTimers set in sync. Callers should
// use these instead of poking t.StartTimer() / t.StopTimer() directly.
func (s *Store) startTimer(id string) {
	t := s.get(id)
	if t == nil {
		return
	}
	t.StartTimerBy(s.timerOwner(t))
	s.ensureTasks()
	s.runningTimers[id] = struct{}{}
}

// stampRunningTimersSeen mirrors the DB-side heartbeat (heartbeatRunningTimers)
// onto the in-memory running entries. The DB heartbeat alone is not enough:
// the in-memory LastSeen otherwise stays at its StartTimer value, and any
// debounced save of that task (title edit, new comment) would upsert the row
// with the stale value — wiping the heartbeat and leaving a live timer looking
// abandoned to a concurrent CLI's stale-timer recovery once it's been running
// longer than idleThreshold. Deliberately does NOT mark anything dirty: the
// stamp only keeps future saves honest, it must not trigger one.
func (s *Store) stampRunningTimersSeen(now time.Time) {
	for id := range s.runningTimers {
		t := s.get(id)
		if t == nil {
			continue
		}
		if e := t.RunningEntryBy(s.timerOwner(t)); e != nil {
			e.LastSeen = now
		}
	}
}

func (s *Store) stopTimer(id string) {
	t := s.get(id)
	if t == nil {
		return
	}
	t.StopTimerBy(s.timerOwner(t))
	delete(s.runningTimers, id)
}

func (s *Store) get(id string) *todo.Todo {
	if s.tasks == nil {
		return nil
	}
	return s.tasks[id]
}

// add stores a value copy of t and returns the pointer the store will hold.
// Callers that want to mutate further should use the returned pointer (or
// re-fetch via get(id)); mutating the value passed in does not affect the
// store. Maintained indexes (subtaskOf, runningTimers) are updated here.
func (s *Store) add(t todo.Todo) *todo.Todo {
	s.ensureTasks()
	cp := t
	s.tasks[cp.ID] = &cp
	if cp.ParentID != "" {
		s.addSubtaskOf(cp.ParentID, &cp)
	}
	if cp.TimerRunningBy(s.timerOwner(&cp)) {
		s.runningTimers[cp.ID] = struct{}{}
	}
	return s.tasks[cp.ID]
}

// setBase records t as the version the store holds (base).
func (s *Store) setBase(t *todo.Todo) { s.setBaseVersion(t, taskVersion(t)) }

// setBaseVersion is setBase with t's taskVersion already worked out, as a
// reload does off the loop.
func (s *Store) setBaseVersion(t *todo.Todo, v uint64) {
	if s.base == nil {
		s.base = make(map[string]*todo.Todo, len(s.tasks))
		s.versions = make(map[string]uint64, len(s.tasks))
	}
	s.dropBase(t.ID)
	s.base[t.ID] = baseCopy(t)
	s.versions[t.ID] = v
	s.baseFingerprint ^= v
}

// dropBase forgets the base of id, if it has one.
func (s *Store) dropBase(id string) {
	if v, ok := s.versions[id]; ok {
		s.baseFingerprint ^= v
		delete(s.versions, id)
	}
	delete(s.base, id)
}

// replace puts t in place of the stored task with its ID, keeping the
// pointer, and the maintained indexes in step with what changed.
func (s *Store) replace(t todo.Todo) {
	cur := s.tasks[t.ID]
	if cur == nil {
		s.add(t)
		return
	}
	if cur.ParentID != t.ParentID {
		s.removeSubtaskOf(cur.ParentID, t.ID)
		*cur = t
		s.addSubtaskOf(t.ParentID, cur)
	} else {
		*cur = t
	}
	if cur.TimerRunningBy(s.timerOwner(cur)) {
		s.runningTimers[t.ID] = struct{}{}
	} else {
		delete(s.runningTimers, t.ID)
	}
}

// remove drops a task and updates every maintained index.
func (s *Store) remove(id string) {
	t := s.tasks[id]
	if t == nil {
		return
	}
	if t.ParentID != "" {
		s.removeSubtaskOf(t.ParentID, id)
	}
	// Children of this task are now orphaned; drop the subtaskOf bucket so
	// callers don't see stale IDs after a cascade delete.
	delete(s.subtaskOf, id)
	delete(s.runningTimers, id)
	delete(s.tasks, id)
}

func (s *Store) len() int { return len(s.tasks) }

// allTodos returns value copies of every task in unspecified order. Used by
// cache rebuilds, selector inputs, and undo snapshots — anywhere the caller
// wants an iterable slice. Each call allocates the slice.
//
// READ-ONLY CONTRACT: these are the live pointers, not copies. Walk them, read
// them, index them — but do not write through them, and do not hold one across
// a mutation that could remove the task. Anything that needs a durable value
// (an undo snapshot, the sync wire format) must copyTodo first, as pushUndo
// does. Pointers rather than values because this runs on every search
// keystroke, and copying a 416-byte struct per task adds up. Callers that
// hold values (the CLI, storage, the sync package, tests) adapt with todoPtrs.
func (s *Store) allTodos() []*todo.Todo {
	out := make([]*todo.Todo, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	return out
}

func (s *Store) markDirty(ids ...string) {
	if s.dirtyIDs == nil {
		s.dirtyIDs = make(map[string]struct{}, len(ids))
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		s.dirtyIDs[id] = struct{}{}
	}
}

// markAllDirty is the fallback used by callers that mutate without naming a
// specific ID (e.g. mass operations not yet refactored to return touched IDs).
// It marks every live task dirty; the save still ends up O(N) until callers
// are precise.
func (s *Store) markAllDirty() {
	if s.dirtyIDs == nil {
		s.dirtyIDs = make(map[string]struct{}, len(s.tasks))
	}
	for id := range s.tasks {
		s.dirtyIDs[id] = struct{}{}
	}
}

// markTombstone records a deletion at the current instant. If the same ID was
// previously marked dirty, the dirty entry is dropped — there is no point
// writing a row we're about to tombstone in the same save. Re-marking an
// already-tombstoned ID keeps the first timestamp: the deletion happened once.
func (s *Store) markTombstone(id string) {
	if id == "" {
		return
	}
	if s.tombstones == nil {
		s.tombstones = make(map[string]time.Time)
	}
	if _, ok := s.tombstones[id]; !ok {
		s.tombstones[id] = time.Now()
	}
	delete(s.dirtyIDs, id)
	// Brought back by an undo, the task is saved whole: against a live base
	// the restore would look like no change at all.
	s.dropBase(id)
}

// changeSet is what one save writes: the dirty tasks, deep-copied so the save
// goroutine sees a stable snapshot while the Update goroutine keeps mutating
// the stored pointers; the base of each that has one (rebase.go); each as it
// was drained, which the save's result is adopted against (adoptSaved); and
// the tombstones.
type changeSet struct {
	dirty      []*todo.Todo
	bases      map[string]*todo.Todo
	sent       map[string]*todo.Todo
	tombstones map[string]time.Time
}

func (c changeSet) empty() bool { return len(c.dirty) == 0 && len(c.tombstones) == 0 }

// drainDirty extracts the current dirty set and tombstones for a save.
func (s *Store) drainDirty() changeSet {
	var c changeSet
	if n := len(s.dirtyIDs); n > 0 {
		c.dirty = make([]*todo.Todo, 0, n)
		c.bases = make(map[string]*todo.Todo, n)
		c.sent = make(map[string]*todo.Todo, n)
		for id := range s.dirtyIDs {
			t := s.tasks[id]
			if t == nil {
				continue
			}
			cp := copyTodo(*t)
			c.dirty = append(c.dirty, &cp)
			c.sent[id] = baseCopy(t)
			if b := s.base[id]; b != nil {
				c.bases[id] = b
			}
			// The copy takes the Auto note to the save; the next edit of
			// this task is someone's own unless flagged again.
			t.Auto = false
		}
	}
	if n := len(s.tombstones); n > 0 {
		c.tombstones = make(map[string]time.Time, n)
		for id, at := range s.tombstones {
			c.tombstones[id] = at
		}
	}
	s.dirtyIDs = nil
	s.tombstones = nil
	return c
}

// ── Undo ──────────────────────────────────────────────────────────────────────

const maxUndoStack = 20

// undoEntry is either a partial snapshot (the previous state of one or more
// specific task IDs — patch-like, O(touched)) or a full snapshot (every task,
// O(N) — used by mass operations or when callers don't name specific IDs).
// On undo we restore the captured tasks: missing IDs in a partial snapshot mean
// "newly created since" → those tasks should be removed.
type undoEntry struct {
	desc    string
	full    []todo.Todo // populated only when partial is nil; legacy fallback
	partial []todo.Todo // captured "before" states for the named IDs
	ids     []string    // IDs the partial entry covers (superset of partial IDs)
}

// pushUndo records the current state of the named task IDs as a patch-style
// snapshot. With no IDs, falls back to a full deep-copy of every task — O(N)
// memory, used by mass operations like global tag rename where many tasks
// change and per-ID tracking would be larger than the snapshot.
func (s *Store) pushUndo(desc string, ids ...string) {
	var entry undoEntry
	entry.desc = desc
	if len(ids) == 0 {
		full := make([]todo.Todo, 0, len(s.tasks))
		for _, t := range s.tasks {
			full = append(full, copyTodo(*t))
		}
		entry.full = full
	} else {
		entry.ids = append([]string(nil), ids...)
		entry.partial = make([]todo.Todo, 0, len(ids))
		for _, id := range ids {
			if t := s.tasks[id]; t != nil {
				entry.partial = append(entry.partial, copyTodo(*t))
			}
		}
	}
	s.undoStack = append(s.undoStack, entry)
	if len(s.undoStack) > maxUndoStack {
		copy(s.undoStack, s.undoStack[1:])
		s.undoStack = s.undoStack[:maxUndoStack]
	}
	// Persist the last few task/subtask deletions so they survive a restart —
	// the user expects deletions to be reversible even after closing tjek,
	// since they're the destructive op with no in-app fallback. Other undo
	// kinds stay in-memory only. A persist failure is swallowed; the worst
	// case is losing the cross-restart safety net, never blocking the delete.
	if isPersistedDelete(entry.desc) {
		_ = savePersistedUndoEntries(s.undoStack)
	}
}

func (s *Store) popUndo() (undoEntry, bool) {
	if len(s.undoStack) == 0 {
		return undoEntry{}, false
	}
	entry := s.undoStack[len(s.undoStack)-1]
	s.undoStack = s.undoStack[:len(s.undoStack)-1]
	// Keep the sidecar in sync when a persisted delete entry is consumed —
	// otherwise a popped delete could resurrect on next start.
	if isPersistedDelete(entry.desc) {
		_ = savePersistedUndoEntries(s.undoStack)
	}
	return entry, true
}

// forget drops the tasks ids from the store, the change set and the undo
// history, for tasks removed from the database outright (undoWithout).
func (s *Store) forget(ids []string) {
	if len(ids) == 0 {
		return
	}
	gone := make(map[string]bool, len(ids))
	for _, id := range ids {
		gone[id] = true
		s.remove(id)
		delete(s.dirtyIDs, id)
		delete(s.tombstones, id)
		s.dropBase(id)
	}
	s.undoStack = undoWithout(s.undoStack, gone)
}

// restoreFromUndo applies an entry. For a full snapshot the entire task map
// is rebuilt. For a partial snapshot only the named IDs are touched: each
// captured task is restored to its prior value, and any ID in entry.ids that
// has no captured "before" state is removed (it was created after the push).
// Callers (performUndo) compute the inverse set-difference for tombstoning
// when restoring a full snapshot.
func (s *Store) restoreFromUndo(entry undoEntry) {
	if entry.partial != nil || entry.ids != nil {
		captured := make(map[string]*todo.Todo, len(entry.partial))
		for i := range entry.partial {
			t := entry.partial[i]
			captured[t.ID] = &t
		}
		for _, id := range entry.ids {
			if before, ok := captured[id]; ok {
				// Restore prior state in place by replacing the map entry.
				// remove() wipes subtaskOf[id], but this task's children are
				// not part of the entry — without re-attaching the bucket they
				// stay live in the map yet unreachable from every subtask view
				// until restart. (Children that ARE in the entry re-insert
				// themselves via their own add.)
				children := s.subtaskOf[id]
				s.remove(id)
				s.add(*before)
				for _, cid := range children {
					if c := s.tasks[cid]; c != nil && c.ParentID == id {
						s.addSubtaskOf(id, c)
					}
				}
			} else {
				// Created after the push — undo means remove.
				s.remove(id)
			}
		}
		return
	}
	// Full snapshot fallback.
	s.tasks = make(map[string]*todo.Todo, len(entry.full))
	s.subtaskOf = make(map[string][]string)
	s.runningTimers = make(map[string]struct{})
	for i := range entry.full {
		s.add(entry.full[i])
	}
}

// taskSetFingerprint hashes the identity and version of a task set
// (taskVersion), folded so that two sets of the same task versions match
// whatever order they arrive in. It is deliberately not a content hash: it
// runs on a reload, on the loop.
func taskSetFingerprint(seq func(yield func(t *todo.Todo))) uint64 {
	// Folded commutatively (XOR of per-task hashes) so iteration order — a
	// map on one side, a slice on the other — cannot change the result.
	var acc uint64
	seq(func(t *todo.Todo) { acc ^= taskVersion(t) })
	return acc
}

// versionSeed keys taskVersion's hashes; one seed per process, so the two
// sides a reload compares hash alike.
var versionSeed = maphash.MakeSeed()

// taskVersion hashes a task as the app shows it: every field, tag and
// dependency, and each comment and time entry. It is a content hash because
// nothing cheaper moves with every change: a merge keeps the later of two
// ModifiedAts, so an edit merged in from a device whose clock is behind
// changes a field and leaves it where it was, and a live load carries no
// stamps. It runs off the loop for a reload (loadedFingerprint) and once per
// task saved, so its cost never lands on a keystroke. A running timer's
// heartbeat is left out: the app and the store each stamp it, at slightly
// different instants. Comments and time entries fold by XOR, since the
// store keeps no order for them.
func taskVersion(t *todo.Todo) uint64 {
	var h maphash.Hash
	h.SetSeed(versionSeed)
	var buf [8]byte
	num := func(h *maphash.Hash, n int64) {
		binary.LittleEndian.PutUint64(buf[:], uint64(n))
		h.Write(buf[:])
	}
	str := func(h *maphash.Hash, s string) {
		num(h, int64(len(s)))
		h.WriteString(s)
	}
	at := func(h *maphash.Hash, x time.Time) { num(h, x.UnixNano()) }
	for _, s := range []string{t.ID, t.Title, t.Notes, t.Project, t.ParentID, t.Recurrence, t.Stage} {
		str(&h, s)
	}
	for _, n := range []int64{int64(t.Status), int64(t.Priority), int64(t.Size), int64(t.SeqRankAtDone), int64(t.RecurIndex), int64(len(t.History))} {
		num(&h, n)
	}
	if t.Deleted {
		num(&h, 1)
	}
	for _, x := range []time.Time{t.CreatedAt, t.ModifiedAt, t.CompletedAt, t.StartDate, t.DueDate, t.RecurFrom, t.DeletedAt} {
		at(&h, x)
	}
	for _, set := range [][]string{t.Tags, t.Dependencies} {
		num(&h, int64(len(set)))
		for _, x := range set {
			str(&h, x)
		}
	}
	var records uint64
	var r maphash.Hash
	r.SetSeed(versionSeed)
	for _, c := range t.Comments {
		r.Reset()
		str(&r, c.ID)
		str(&r, c.Text)
		at(&r, c.CreatedAt)
		at(&r, c.ModifiedAt)
		at(&r, c.DeletedAt)
		records ^= r.Sum64()
	}
	for _, e := range t.TimeEntries {
		r.Reset()
		str(&r, e.ID)
		at(&r, e.StartedAt)
		at(&r, e.StoppedAt)
		at(&r, e.ModifiedAt)
		at(&r, e.DeletedAt)
		records ^= r.Sum64()
	}
	num(&h, int64(records))
	return h.Sum64()
}

// sameAsLoaded reports whether a freshly loaded task set holds exactly the task
// versions the Store already has. theirs is loadedFingerprint(loaded), which
// the reload works out off the loop; zero has it worked out here. When every
// task has a base, the Store's side is baseFingerprint, kept as bases change,
// so a reload that brings nothing new costs a comparison; the caller rules
// out unsaved edits, which the bases predate.
func (s *Store) sameAsLoaded(loaded []todo.Todo, theirs uint64) bool {
	if len(loaded) != len(s.tasks) {
		return false
	}
	if theirs == 0 {
		theirs, _ = loadedFingerprint(loaded)
	}
	mine := s.baseFingerprint
	if len(s.versions) != len(s.tasks) {
		mine = taskSetFingerprint(func(yield func(*todo.Todo)) {
			for _, t := range s.tasks {
				yield(t)
			}
		})
	}
	return mine == theirs
}

// loadedFingerprint is taskSetFingerprint of a loaded task set, with each
// task's taskVersion, in order, for the bases a swap sets.
func loadedFingerprint(loaded []todo.Todo) (uint64, []uint64) {
	versions := make([]uint64, len(loaded))
	var fp uint64
	for i := range loaded {
		versions[i] = taskVersion(&loaded[i])
		fp ^= versions[i]
	}
	return fp, versions
}
