package todo

import (
	"sort"
	"time"
)

// Event is one line of a task's history: who did what to it, and when. The
// save that writes an edit records it (see stamps.go in the app), so every
// way of editing a task, the TUI, the CLI, an import, leaves the same trail.
// Events are never edited or deleted; a sync merges them as a union by ID.
type Event struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
	// Author is the name of the person whose device made the change, as set
	// in Settings (or TJEK_AUTHOR). Empty for an event from a device with no
	// name known.
	Author string `json:"author,omitempty"`
	// Source says how the change was made when it was not by hand in the
	// app: SourceCLI, SourceWeb or SourceAuto.
	Source string `json:"source,omitempty"`
	Action string `json:"action"`
	// Fields names what an edit changed: Field keys, plus FieldTags and
	// FieldDependencies for the sets. Also set on another action when the
	// same save changed more than that (a close that moved the due date).
	Fields []string `json:"fields,omitempty"`
}

// Event actions.
const (
	ActionCreated  = "created"
	ActionEdited   = "edited"
	ActionClosed   = "closed"
	ActionReopened = "reopened"
	ActionDeleted  = "deleted"
	ActionRestored = "restored"
)

// Event sources. The zero value is a change made by hand in the app.
const (
	SourceCLI  = "cli"
	SourceWeb  = "web"
	SourceAuto = "auto"
)

// Keys for the set members in Event.Fields, beside the Field keys.
const (
	FieldTags         = "tags"
	FieldDependencies = "dependencies"
)

// SortHistory orders events oldest first, by time and then ID, so two
// devices that hold the same events list them the same way.
func SortHistory(h []Event) {
	sort.Slice(h, func(i, j int) bool {
		if !h[i].At.Equal(h[j].At) {
			return h[i].At.Before(h[j].At)
		}
		return h[i].ID < h[j].ID
	})
}

// MergeHistory is the union of a and b by ID, sorted. An event is written
// once and never changed, so two copies with one ID are the same event.
func MergeHistory(a, b []Event) []Event {
	if len(b) == 0 {
		return a
	}
	if len(a) == 0 {
		return b
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]Event, 0, len(a)+len(b))
	for _, list := range [][]Event{a, b} {
		for _, e := range list {
			if !seen[e.ID] {
				seen[e.ID] = true
				out = append(out, e)
			}
		}
	}
	SortHistory(out)
	return out
}
