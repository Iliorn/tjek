package app

import (
	"slices"

	"github.com/Iliorn/tjek/todo"
)

// rebase.go writes an edit made in memory onto the task as the store holds it
// now, rather than writing the copy in memory over it.
//
// The app holds every task in memory and saves the ones it changed. Between
// loading a task and saving it, a sync, a shared project's pass or another
// process may change the stored row, and the copy in memory does not know.
// Saving that copy whole would write its out-of-date fields back, stamped as
// new edits that win every later merge, and tombstone the comments and time
// entries it never saw. So the store keeps, for each task, the version this
// copy last agreed with the store on (Store.base), and a save writes only
// what differs from it: each scalar unit (todo.Fields), each tag and
// dependency, each comment and time entry by ID. Everything else stays as
// stored. A task with no base (new here, or brought back from a delete) is
// saved whole.
//
// The same fold brings a save's result back into memory (adoptSaved) and a
// reload under a task still being edited (handleReloaded), so the copy in
// memory catches up without losing what was typed since.

// rebase is stored with the edits that turned base into mine. When mine
// changed a unit that stored changed too, mine wins: it is the later edit,
// as the sync merge would also decide.
func rebase(stored, base, mine *todo.Todo) todo.Todo {
	out := copyTodo(*stored)
	for _, f := range todo.Fields {
		if !f.Same(base, mine) {
			f.Copy(&out, mine)
		}
	}
	out.Tags = rebaseSet(stored.Tags, base.Tags, mine.Tags)
	out.Dependencies = rebaseSet(stored.Dependencies, base.Dependencies, mine.Dependencies)
	out.Comments = rebaseRecords(stored.Comments, base.Comments, mine.Comments,
		func(c todo.Comment) string { return c.ID }, sameComment)
	out.TimeEntries = rebaseRecords(stored.TimeEntries, base.TimeEntries, mine.TimeEntries,
		func(e todo.TimeEntry) string { return e.ID }, sameTimeEntry)
	out.History = todo.MergeHistory(stored.History, mine.History)
	if out.CreatedAt.IsZero() {
		out.CreatedAt = mine.CreatedAt
	}
	if mine.ModifiedAt.After(out.ModifiedAt) {
		out.ModifiedAt = mine.ModifiedAt
	}
	out.Auto = mine.Auto
	return out
}

// rebaseSet is stored with the members mine added to base and without the
// ones it removed, in stored's order with the additions after.
func rebaseSet(stored, base, mine []string) []string {
	var out []string
	for _, x := range stored {
		if slices.Contains(base, x) && !slices.Contains(mine, x) {
			continue // removed here
		}
		out = append(out, x)
	}
	for _, x := range mine {
		if !slices.Contains(base, x) && !slices.Contains(out, x) {
			out = append(out, x) // added here
		}
	}
	return out
}

// rebaseRecords folds comments or time entries by ID: one mine added or
// changed is mine's, one mine removed is left out (so the save tombstones
// it), and one mine left alone is stored's, or stays gone when the store no
// longer holds it. stored is the live records only.
func rebaseRecords[T any](stored, base, mine []T, id func(T) string, same func(a, b T) bool) []T {
	index := func(xs []T) map[string]T {
		m := make(map[string]T, len(xs))
		for _, x := range xs {
			m[id(x)] = x
		}
		return m
	}
	inBase, inMine := index(base), index(mine)
	out := make([]T, 0, len(stored)+len(mine))
	seen := make(map[string]bool, len(stored))
	for _, s := range stored {
		k := id(s)
		seen[k] = true
		m, mok := inMine[k]
		b, bok := inBase[k]
		switch {
		case mok && bok && same(b, m):
			out = append(out, s) // untouched here
		case mok:
			out = append(out, m) // changed or added here
		case bok:
			// removed here
		default:
			out = append(out, s) // added elsewhere
		}
	}
	for _, m := range mine {
		k := id(m)
		if seen[k] {
			continue
		}
		if b, bok := inBase[k]; bok && same(b, m) {
			continue // removed elsewhere, untouched here
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sameComment and sameTimeEntry compare what an edit changes. The author is
// left out: a save fills it in on the copy in memory (adoptSaved), and that
// is not an edit.
func sameComment(a, b todo.Comment) bool {
	return a.Text == b.Text && a.CreatedAt.Equal(b.CreatedAt) &&
		a.ModifiedAt.Equal(b.ModifiedAt) && a.DeletedAt.Equal(b.DeletedAt)
}

func sameTimeEntry(a, b todo.TimeEntry) bool {
	return a.StartedAt.Equal(b.StartedAt) && a.StoppedAt.Equal(b.StoppedAt) &&
		a.ModifiedAt.Equal(b.ModifiedAt) && a.DeletedAt.Equal(b.DeletedAt) &&
		a.LastSeen.Equal(b.LastSeen)
}

// sameContent reports whether a and b agree on everything a rebase decides
// on, so adopting one over the other would change nothing the screen shows.
func sameContent(a, b *todo.Todo) bool {
	for _, f := range todo.Fields {
		if !f.Same(a, b) {
			return false
		}
	}
	return slices.Equal(a.Tags, b.Tags) && slices.Equal(a.Dependencies, b.Dependencies) &&
		slices.EqualFunc(a.Comments, b.Comments, sameComment) &&
		slices.EqualFunc(a.TimeEntries, b.TimeEntries, sameTimeEntry)
}

// baseCopy is t as a base holds it: what rebase compares, in slices of its
// own, so an edit to t's comments in place cannot reach it. Stamps and
// history are left out; a rebase takes both from the store.
func baseCopy(t *todo.Todo) *todo.Todo {
	cp := *t
	cp.Stamps, cp.History = nil, nil
	cp.Tags = slices.Clone(t.Tags)
	cp.Dependencies = slices.Clone(t.Dependencies)
	cp.Comments = slices.Clone(t.Comments)
	cp.TimeEntries = slices.Clone(t.TimeEntries)
	return &cp
}
