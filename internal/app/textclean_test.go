package app

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
	"github.com/charmbracelet/x/ansi"
)

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":         "plain text",
		"tab\there":          "tab here",
		"line\nbreak":        "line break",
		"cr\rhere":           "cr here",
		"bell\x07":           "bell",
		"esc\x1b[31mred":     "esc[31mred",
		"c1\u0085next\u009b": "c1next",
		"Køb mælk · ✓ 🎉 漢字":  "Køb mælk · ✓ 🎉 漢字",
		"del\x7f":            "del",
	} {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

// A description edited on Windows comes back from the editor with \r\n line
// ends; each \r left in a line sent the cursor back over the detail pane's
// border when it was drawn.
func TestNotesFromTheEditorLoseTheirCarriageReturns(t *testing.T) {
	setTestHome(t, t.TempDir())
	id := "crlf-task"
	if err := writeNotesFile(id, "first\r\nsecond\r\nthird\rfourth\r\n"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(notesFilePath(id)) })
	got, err := readNotesFile(id)
	if err != nil {
		t.Fatal(err)
	}
	if want := "first\nsecond\nthird\nfourth\n"; got != want {
		t.Errorf("readNotesFile = %q, want %q", got, want)
	}
	if got, _ := noteFlagText("-", strings.NewReader("piped\r\nnote\r\n")); got != "piped\nnote" {
		t.Errorf("noteFlagText = %q, want the \\r gone", got)
	}
}

func TestTagsDropControlCharacters(t *testing.T) {
	if got := todo.NormalizeTag("#ho\x07me\tWork"); got != "home-work" {
		t.Errorf("NormalizeTag = %q, want home-work", got)
	}
}

// Control characters in any of a task's texts, from a paste, an editor, an
// import or another device, stay off the screen on every tab: no line grows
// past the window, no control character reaches the terminal, and no escape
// sequence of the task's own is passed through.
func TestControlCharactersStayOffTheScreen(t *testing.T) {
	bad := "A\tB\rC\x07D\x1b[31mE\u0085F"
	day := startOfDay(time.Now())
	parent := todo.New("Parent " + bad)
	parent.Project = "Proj" + bad
	parent.Tags = []string{"tag" + bad} // as an old store or another device may hold it
	parent.SetNotes("note " + bad + "\nline2 " + bad)
	parent.AddComment("comment " + bad)
	parent.DueDate = day.AddDate(0, 0, 1)
	parent.TimeEntries = []todo.TimeEntry{{ID: "e1", StartedAt: day.Add(9 * time.Hour), StoppedAt: day.Add(10 * time.Hour)}}
	child := todo.New("Child " + bad)
	child.ParentID = parent.ID
	dep := todo.New("Dep " + bad)
	parent.AddDependency(dep.ID)
	base := modelWithTasks(t, parent, child, dep)

	right := []string{"right"}
	views := map[string][]string{"tasks": nil, "board": {"5"}, "stats": {"6"},
		"calendar": {"2"}, "calendar entries": {"2", "enter"},
		"tags": {"3"}, "tag's tasks": {"3", "enter"}, "projects": {"4"}, "project's tasks": {"4", "enter"}}
	keys := []string{"enter"}
	for _, s := range []string{"fields", "tags", "subtasks", "dependencies", "time", "comments", "history"} {
		views["detail "+s] = append([]string(nil), keys...)
		keys = append(keys, right...)
	}
	for _, pos := range []detailPos{detailRight, detailBottom} {
		for name, keys := range views {
			m := base
			m.detailPos = pos
			m.termWidth, m.termHeight = 130, 45
			m.calendar.selected = day
			// sendKey, not script: the "[31m" left of the title's escape
			// is the text this test expects on screen, and script fails a
			// screen that shows one (noStrayCodes).
			for _, k := range keys {
				m = sendKey(t, m, k)
			}
			for i, line := range strings.Split(m.View(), "\n") {
				var probs []string
				if w := ansi.StringWidth(line); w > m.termWidth {
					probs = append(probs, fmt.Sprintf("%d wide", w))
				}
				if strings.Contains(line, "\x1b[31mE") {
					probs = append(probs, "the task's own escape sequence")
				}
				for _, r := range ansi.Strip(line) {
					if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
						probs = append(probs, fmt.Sprintf("control %U", r))
						break
					}
				}
				if len(probs) > 0 {
					t.Errorf("detail %v, %s, line %d: %s\n%q", pos, name, i, strings.Join(probs, ", "), ansi.Strip(line))
				}
			}
		}
	}
}
