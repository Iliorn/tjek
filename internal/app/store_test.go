package app

import (
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
)

// A reload that carries the task versions the Store already has must be
// recognised as a no-op: the watcher fires on our own writes, and rebuilding
// the Store for those costs milliseconds on the event loop.
func TestSameAsLoadedRecognisesAnUnchangedSnapshot(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	a := todo.New("alpha")
	a.ID, a.ModifiedAt = "a", base
	b := todo.New("beta")
	b.ID, b.ModifiedAt = "b", base.Add(time.Minute)

	var s Store
	s.ensureTasks()
	s.add(a)
	s.add(b)

	// Same versions, reversed order — the load order is not part of identity.
	if !s.sameAsLoaded([]todo.Todo{b, a}, 0) {
		t.Error("an identical snapshot was reported as changed")
	}
	if s.sameAsLoaded([]todo.Todo{a}, 0) {
		t.Error("a snapshot missing a task was reported as unchanged")
	}
	c := todo.New("gamma")
	c.ID, c.ModifiedAt = "c", base
	if s.sameAsLoaded([]todo.Todo{a, b, c}, 0) {
		t.Error("a snapshot with an extra task was reported as unchanged")
	}
	// An edit elsewhere bumps ModifiedAt, which is what makes it visible here.
	edited := b
	edited.Title = "beta, edited"
	edited.ModifiedAt = base.Add(2 * time.Minute)
	if s.sameAsLoaded([]todo.Todo{a, edited}, 0) {
		t.Error("an edited task was reported as unchanged")
	}
	// A tombstone arriving for a live task is a change too.
	dead := a
	dead.DeletedAt = time.Now()
	if s.sameAsLoaded([]todo.Todo{dead, b}, 0) {
		t.Error("a tombstone was reported as unchanged")
	}
	// A merge keeps the later ModifiedAt, so an edit from a device whose
	// clock is behind leaves it where it was; the content moved.
	behind := copyTodo(b)
	behind.Title = "beta, from a slow clock"
	if s.sameAsLoaded([]todo.Todo{a, behind}, 0) {
		t.Error("a field merged in from a slow clock was reported as unchanged")
	}
	commented := copyTodo(b)
	commented.Comments = []todo.Comment{{ID: "c1", Text: "hi", CreatedAt: base, ModifiedAt: base}}
	if s.sameAsLoaded([]todo.Todo{a, commented}, 0) {
		t.Error("a comment merged in was reported as unchanged")
	}
}

// A running timer's heartbeat is stamped by the app and the store each, at
// slightly different instants; it is not a change worth a rebuild.
func TestSameAsLoadedIgnoresTheHeartbeat(t *testing.T) {
	task := todo.New("tracked")
	task.TimeEntries = []todo.TimeEntry{{ID: "e1", StartedAt: time.Now(), LastSeen: time.Now()}}
	var s Store
	s.ensureTasks()
	s.add(copyTodo(task))
	task.TimeEntries[0].LastSeen = task.TimeEntries[0].LastSeen.Add(time.Second)
	if !s.sameAsLoaded([]todo.Todo{task}, 0) {
		t.Error("a heartbeat alone was reported as a change")
	}
}

// Every unit a merge decides on moves taskVersion, so a field added to the
// task later cannot be left out of what a reload compares.
func TestTaskVersionCoversEveryField(t *testing.T) {
	base := todo.New("a")
	other := todo.New("b")
	other.Notes, other.Priority, other.Size = "n", todo.PriorityHigh, todo.SizeLarge
	other.Project, other.ParentID, other.Recurrence, other.Stage = "p", "x", "weekly", "Doing"
	other.DueDate, other.StartDate = time.Now().Add(time.Hour), time.Now()
	other.Status, other.CompletedAt, other.SeqRankAtDone = todo.Done, time.Now(), 3
	other.Deleted, other.DeletedAt = true, time.Now()
	for _, f := range todo.Fields {
		x := copyTodo(base)
		f.Copy(&x, &other)
		if f.Same(&x, &base) {
			t.Fatalf("setup: %s does not differ between the two tasks", f.Key)
		}
		if taskVersion(&x) == taskVersion(&base) {
			t.Errorf("a change to %s leaves taskVersion where it was", f.Key)
		}
	}
}
