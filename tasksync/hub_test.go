package tasksync

import (
	"reflect"
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
)

func TestStoreDigestOrderIndependent(t *testing.T) {
	a := todo.New("alpha")
	a.Tags = []string{"x", "y"}
	b := todo.New("beta")

	// Same content, different task order and different tag order → same digest.
	a2 := a
	a2.Tags = []string{"y", "x"}
	set1 := []todo.Todo{a, b}
	set2 := []todo.Todo{b, a2}
	if StoreDigest(set1) != StoreDigest(set2) {
		t.Errorf("digest should ignore task/tag ordering")
	}

	// A real change → different digest.
	changed := a
	changed.Title = "alpha edited"
	if StoreDigest([]todo.Todo{a, b}) == StoreDigest([]todo.Todo{changed, b}) {
		t.Errorf("digest should change when a title changes")
	}
}

func TestStoreDigestLocationInsensitive(t *testing.T) {
	loc := time.FixedZone("CEST", 2*60*60)
	a := todo.New("alpha")
	a.DueDate = time.Date(2030, 5, 20, 0, 0, 0, 0, loc)
	a.AddComment("note")
	a.TimeEntries = []todo.TimeEntry{{ID: "te1", StartedAt: a.CreatedAt, StoppedAt: a.ModifiedAt}}

	// The same instants rehydrated in a different zone (as parseTime does
	// after a store round-trip) must hash identically.
	shifted := a
	shifted.CreatedAt = a.CreatedAt.UTC()
	shifted.ModifiedAt = a.ModifiedAt.UTC()
	shifted.DueDate = a.DueDate.UTC()
	shifted.Comments = []todo.Comment{a.Comments[0]}
	shifted.Comments[0].CreatedAt = a.Comments[0].CreatedAt.UTC()
	shifted.TimeEntries = []todo.TimeEntry{a.TimeEntries[0]}
	shifted.TimeEntries[0].StartedAt = a.TimeEntries[0].StartedAt.UTC()
	shifted.TimeEntries[0].StoppedAt = a.TimeEntries[0].StoppedAt.UTC()

	if StoreDigest([]todo.Todo{a}) != StoreDigest([]todo.Todo{shifted}) {
		t.Errorf("digest should compare timestamps as instants, not zone-tagged strings")
	}
	if got, want := CanonicalJSON(a), CanonicalJSON(shifted); string(got) != string(want) {
		t.Errorf("CanonicalJSON differs across zones:\n%s\n%s", got, want)
	}
}

// TestCanonicalJSONCoversEveryTime sets every time on a task, its comments,
// time entries and history, in two zones, so a time field added to any of
// them that canonicalizeForDigest leaves in its zone fails here rather than
// in two devices rewriting a shared project to each other for good.
func TestCanonicalJSONCoversEveryTime(t *testing.T) {
	at := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	in := func(zone *time.Location) todo.Todo {
		task := todo.Todo{ID: "a", Comments: make([]todo.Comment, 1),
			TimeEntries: make([]todo.TimeEntry, 1), History: make([]todo.Event, 1)}
		setTimes(reflect.ValueOf(&task).Elem(), at.In(zone))
		return task
	}
	east, west := in(time.FixedZone("UTC+2", 2*3600)), in(time.FixedZone("UTC-4", -4*3600))
	if got, want := CanonicalJSON(west), CanonicalJSON(east); string(got) != string(want) {
		t.Errorf("CanonicalJSON differs across zones:\n%s\n%s", got, want)
	}
}

// setTimes sets every time.Time in v, a struct, to at, through its structs
// and the elements of its slices.
func setTimes(v reflect.Value, at time.Time) {
	for i := range v.NumField() {
		switch f := v.Field(i); {
		case f.Type() == reflect.TypeOf(time.Time{}):
			f.Set(reflect.ValueOf(at))
		case f.Kind() == reflect.Struct:
			setTimes(f, at)
		case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.Struct:
			for j := range f.Len() {
				setTimes(f.Index(j), at)
			}
		}
	}
}

func TestStoreDigestDoesNotMutateInput(t *testing.T) {
	a := todo.New("alpha")
	a.Tags = []string{"z", "a"}
	_ = StoreDigest([]todo.Todo{a})
	if a.Tags[0] != "z" || a.Tags[1] != "a" {
		t.Errorf("storeDigest must not reorder the caller's slices, got %v", a.Tags)
	}
}

func TestSSEHubCapAndNonBlocking(t *testing.T) {
	h := NewHub()
	for i := 0; i < sseMaxClients; i++ {
		if h.Subscribe() == nil {
			t.Fatalf("subscribe %d should succeed under the cap", i)
		}
	}
	if h.Subscribe() != nil {
		t.Errorf("subscribe past the cap must return nil")
	}

	// Broadcast must not block even when every buffer is already full.
	done := make(chan struct{})
	go func() { h.Broadcast(); h.Broadcast(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked on full subscriber buffers")
	}
}
