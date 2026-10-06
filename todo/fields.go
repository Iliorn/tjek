package todo

import (
	"strings"

	"github.com/Iliorn/tjek/hlc"
)

// fields.go names the units a sync merge decides on, one Stamps key each.
//
// A scalar field is one unit, or several fields that change together:
// closing a task sets its status, completion time and rank ("done"), and a
// deletion its flag and time ("deleted"). Tags and dependencies are sets, so
// each member is a unit of its own ("tag:<name>", "dep:<id>"), and a member
// that was removed keeps its stamp so the removal can win over an older add.
// Comments and time entries are not here: they carry their own IDs and
// modification times, and merge as records.

// Field is one scalar unit: its Stamps key, whether two tasks agree on it,
// and how to copy it from one task to another.
type Field struct {
	Key  string
	Same func(a, b *Todo) bool
	Copy func(dst, src *Todo)
}

// Fields is every scalar unit, in the order a merge visits them.
// TestFieldsCoverTheTodo fails for a Todo field that is in no unit, so a new
// field cannot be forgotten by the merge.
var Fields = []Field{
	{"title", func(a, b *Todo) bool { return a.Title == b.Title }, func(d, s *Todo) { d.Title = s.Title }},
	{"notes", func(a, b *Todo) bool { return a.Notes == b.Notes }, func(d, s *Todo) { d.Notes = s.Notes }},
	{"priority", func(a, b *Todo) bool { return a.Priority == b.Priority }, func(d, s *Todo) { d.Priority = s.Priority }},
	{"size", func(a, b *Todo) bool { return a.Size == b.Size }, func(d, s *Todo) { d.Size = s.Size }},
	{"project", func(a, b *Todo) bool { return a.Project == b.Project }, func(d, s *Todo) { d.Project = s.Project }},
	{"parent", func(a, b *Todo) bool { return a.ParentID == b.ParentID }, func(d, s *Todo) { d.ParentID = s.ParentID }},
	{"recurrence",
		func(a, b *Todo) bool {
			return a.Recurrence == b.Recurrence && a.RecurFrom.Equal(b.RecurFrom) && a.RecurIndex == b.RecurIndex
		},
		func(d, s *Todo) { d.Recurrence, d.RecurFrom, d.RecurIndex = s.Recurrence, s.RecurFrom, s.RecurIndex }},
	{"stage", func(a, b *Todo) bool { return a.Stage == b.Stage }, func(d, s *Todo) { d.Stage = s.Stage }},
	{"due", func(a, b *Todo) bool { return a.DueDate.Equal(b.DueDate) }, func(d, s *Todo) { d.DueDate = s.DueDate }},
	{"start", func(a, b *Todo) bool { return a.StartDate.Equal(b.StartDate) }, func(d, s *Todo) { d.StartDate = s.StartDate }},
	{"done",
		func(a, b *Todo) bool {
			return a.Status == b.Status && a.CompletedAt.Equal(b.CompletedAt) && a.SeqRankAtDone == b.SeqRankAtDone
		},
		func(d, s *Todo) { d.Status, d.CompletedAt, d.SeqRankAtDone = s.Status, s.CompletedAt, s.SeqRankAtDone }},
	{"deleted",
		func(a, b *Todo) bool { return a.Deleted == b.Deleted },
		func(d, s *Todo) { d.Deleted, d.DeletedAt = s.Deleted, s.DeletedAt }},
}

// Set member keys: TagKey("home") is "tag:home", DepKey(id) "dep:<id>".
//
// SetsKey is the task's membership baseline: every tag and dependency with no
// stamp of its own was absent as of this stamp. It is what a removal made
// before stamps existed, or a member a device never heard of, is ordered by.
const (
	TagKeyPrefix = "tag:"
	DepKeyPrefix = "dep:"
	SetsKey      = "sets"
)

func TagKey(name string) string { return TagKeyPrefix + name }
func DepKey(id string) string   { return DepKeyPrefix + id }

// Stamp is the stamp of key on t. A task saved before stamps existed has
// none, and gets one reconstructed from the time it records: its last
// modification, or for a deleted task's "deleted" unit the deletion. A set
// member t does not hold and has no stamp for is absent as of t's baseline
// (SetsKey). A reconstructed stamp sorts below any real edit in the same
// millisecond (hlc.At), which is the order the whole-task merge before stamps
// gave them.
func (t *Todo) Stamp(key string) hlc.Stamp {
	if s, ok := t.Stamps[key]; ok {
		return s
	}
	if key == "deleted" && t.Deleted {
		return hlc.At(t.DeletedAt)
	}
	if key == SetsKey || (isSetKey(key) && !t.HasMember(key)) {
		return t.baseline()
	}
	return hlc.At(t.ModifiedAt)
}

// baseline is the stamp as of which t's unlisted set members were absent: its
// recorded baseline, or for a task from before stamps its modification, when
// it was a whole snapshot. A task with stamps and no baseline has none.
func (t *Todo) baseline() hlc.Stamp {
	if s, ok := t.Stamps[SetsKey]; ok {
		return s
	}
	if len(t.Stamps) == 0 {
		return hlc.At(t.ModifiedAt)
	}
	return ""
}

// SetKeys is every set member key t holds or has a stamp for: the members
// present, and the removed ones whose removal it remembers.
func (t *Todo) SetKeys() []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, tag := range t.Tags {
		add(TagKey(tag))
	}
	for _, dep := range t.Dependencies {
		add(DepKey(dep))
	}
	for k := range t.Stamps {
		if isSetKey(k) {
			add(k)
		}
	}
	return out
}

// HasMember reports whether t holds the set member key names.
func (t *Todo) HasMember(key string) bool {
	if name, ok := strings.CutPrefix(key, TagKeyPrefix); ok {
		return contains(t.Tags, name)
	}
	if id, ok := strings.CutPrefix(key, DepKeyPrefix); ok {
		return contains(t.Dependencies, id)
	}
	return false
}

// LatestStamp is the latest of t's stamps, reconstructed ones included: the
// last moment anything about t was set.
func (t *Todo) LatestStamp() hlc.Stamp {
	latest := hlc.At(t.ModifiedAt)
	if t.Deleted {
		latest = hlc.Max(latest, hlc.At(t.DeletedAt))
	}
	for _, s := range t.Stamps {
		latest = hlc.Max(latest, s)
	}
	return latest
}

func isSetKey(k string) bool {
	return strings.HasPrefix(k, TagKeyPrefix) || strings.HasPrefix(k, DepKeyPrefix)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
