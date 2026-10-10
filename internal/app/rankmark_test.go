package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Iliorn/tjek/todo"
)

// b on the Tags tab cycles a tag through higher, lower and as usual: the
// row shows it, its tasks move, the choice is kept, and a rename carries it.
func TestMarkingATagMovesItsTasks(t *testing.T) {
	plain := todo.New("plain task")
	plain.Priority = todo.PriorityHigh
	tagged := todo.New("tagged task")
	tagged.Tags = []string{"next"}
	m := modelWithTasks(t, plain, tagged)
	m.termWidth, m.termHeight = 110, 30
	m.ensureCache()
	if m.cache.active[0].ID != plain.ID {
		t.Fatal("before marking, the high-priority task should lead")
	}

	m = script(t, m, "3")
	m.tagTabCursor = slices.Index(m.getFilteredTagsForTab(), "next")

	// Higher: one priority step brings the medium task level with the high
	// one; as usual it would sit below.
	m = script(t, m, "b")
	if m.rank.Biases.Marks.Get("#next") != 1 {
		t.Fatal("b should mark #next higher")
	}
	if !strings.Contains(ansi.Strip(m.View()), "#next ▲") {
		t.Error("the Tags row should show ▲")
	}
	m.ensureCache()
	if got := m.rank.Components(m.get(tagged.ID)).Marked; got <= 0 {
		t.Errorf("tagged task's Marked = %v, want positive", got)
	}

	// Lower, then as usual.
	m = script(t, m, "b")
	if m.rank.Biases.Marks.Get("#next") != -1 {
		t.Fatal("a second b should mark #next lower")
	}
	s, _ := loadSettings()
	if !slices.Equal(s.RankLower, []string{"#next"}) || len(s.RankHigher) != 0 {
		t.Errorf("settings kept higher %v, lower %v", s.RankHigher, s.RankLower)
	}
	m = script(t, m, "b")
	if m.rank.Biases.Marks.Get("#next") != 0 {
		t.Fatal("a third b should leave #next as usual")
	}

	// A rename moves the mark to the new name, and undo moves it back with
	// the tasks.
	m = script(t, m, "b")
	m.pushUndo("rename tag")
	m.markModified(m.renameTagGlobally("next", "now")...)
	if m.rank.Biases.Marks.Get("#now") != 1 || m.rank.Biases.Marks.Get("#next") != 0 {
		t.Errorf("after rename: marks = %+v", m.rank.Biases.Marks)
	}
	m = script(t, m, "u")
	m.ensureCache()
	if got := m.get(tagged.ID); got == nil || !slices.Contains(got.Tags, "next") ||
		m.rank.Biases.Marks.Get("#next") != 1 || m.rank.Biases.Marks.Get("#now") != 0 {
		t.Errorf("after undo: tags %v, marks %+v", got.Tags, m.rank.Biases.Marks)
	}
	if s, _ := loadSettings(); !slices.Equal(s.RankHigher, []string{"#next"}) {
		t.Errorf("after undo, settings keep higher %v", s.RankHigher)
	}

	// Renaming back carries the latest choice, with nothing left behind.
	m.pushUndo("rename tag")
	m.renameTagGlobally("next", "job")
	m.cycleRankMark("#job") // higher → lower
	m.pushUndo("rename tag")
	m.renameTagGlobally("job", "next")
	if m.rank.Biases.Marks.Get("#next") != -1 || m.rank.Biases.Marks.Get("#job") != 0 {
		t.Errorf("after renaming back: marks = %+v", m.rank.Biases.Marks)
	}
}
