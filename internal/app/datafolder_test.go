package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Iliorn/tjek/paths"
)

// A folder chosen in Settings holds every kind, ahead of the standard places
// and behind TJEK_HOME; forgetting it brings the standard places back.
func TestChosenFolderResolution(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	standard, _ := paths.Dir(paths.Data)

	chosen := filepath.Join(home, "work", "tjek")
	if err := paths.SetChosen(chosen); err != nil {
		t.Fatal(err)
	}
	for _, k := range []paths.Kind{paths.Config, paths.Data, paths.State, paths.Cache} {
		if d, _ := paths.Dir(k); d != chosen {
			t.Errorf("kind %d resolves to %s, want the chosen %s", k, d, chosen)
		}
	}
	if !strings.HasPrefix(paths.LocationPath(), home) || strings.HasPrefix(paths.LocationPath(), chosen) {
		t.Errorf("the choice is kept at %s, which should be in the standard config folder", paths.LocationPath())
	}

	override := filepath.Join(home, "override")
	t.Setenv("TJEK_HOME", override)
	if d, _ := paths.Dir(paths.Data); d != override {
		t.Errorf("TJEK_HOME should win over the chosen folder, got %s", d)
	}
	t.Setenv("TJEK_HOME", "")

	if err := paths.SetChosen(""); err != nil {
		t.Fatal(err)
	}
	if d, _ := paths.Dir(paths.Data); d != standard {
		t.Errorf("with the choice forgotten the data is at %s, want %s", d, standard)
	}

	// A relative path names a different folder from every working directory.
	if err := os.WriteFile(paths.LocationPath(), []byte("relative/tjek\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, _ := paths.Dir(paths.Data); d != standard {
		t.Errorf("a relative choice should be ignored, got %s", d)
	}
}

// Moving into a folder takes the tasks and settings along, sets the old
// folders aside, and points tjek there; moving back files everything into
// its standard place again.
func TestMoveDataThereAndBack(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	if code := runCLI([]string{"add", "Buy milk"}, false); code != 0 {
		t.Fatalf("add exited %d", code)
	}
	if err := saveSettings(appSettings{Name: "Mark"}); err != nil {
		t.Fatal(err)
	}
	checkpointStore()
	closeStore()

	from, _ := currentLayout()
	standard, _ := standardLayout()
	target := filepath.Join(home, "work", "tjek")
	res, err := moveData(from, folderLayout(target), target, "one", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := paths.Dir(paths.Data); d != target {
		t.Fatalf("after the move tjek looks in %s, want %s", d, target)
	}
	for _, name := range []string{"tasks.db", "settings.json"} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Errorf("%s did not come along: %v", name, err)
		}
	}
	if len(res.aside) == 0 || len(res.left) != 0 {
		t.Fatal("the old folders were not set aside")
	}
	for _, dir := range res.aside {
		if !strings.HasSuffix(dir, ".moved-one") {
			t.Errorf("set aside as %s", dir)
		}
	}
	assertMilk(t)
	if s, _ := loadSettings(); s.Name != "Mark" {
		t.Errorf("settings read %q after the move", s.Name)
	}
	closeStore()

	cur, _ := currentLayout()
	if _, err := moveData(cur, standard, "", "two", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if paths.Chosen() != "" {
		t.Error("moving back should forget the chosen folder")
	}
	if _, err := os.Stat(filepath.Join(standard[paths.Data], "tasks.db")); err != nil {
		t.Errorf("the store is not back in the data folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(standard[paths.Config], "settings.json")); err != nil {
		t.Errorf("the settings are not back in the config folder: %v", err)
	}
	assertMilk(t)
}

// A folder Windows will not rename (a file in it held open) does not undo a
// move whose copy is safe: it is left where it was, and tjek reads the new
// place. Moving back later sets that leftover aside instead of refusing.
func TestMoveDataLeavesAFolderThatWillNotRename(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this user cannot rename in")
	}
	home := t.TempDir()
	setTestHome(t, home)
	renameTries, renamePause = 2, time.Millisecond
	t.Cleanup(func() { renameTries, renamePause = 10, 200*time.Millisecond })
	if code := runCLI([]string{"add", "Buy milk"}, false); code != 0 {
		t.Fatalf("add exited %d", code)
	}
	checkpointStore()
	closeStore()
	from, _ := currentLayout()
	standard, _ := standardLayout()

	// The data folder's parent refuses renames, as Windows does for a folder
	// with an open file in it.
	parent := filepath.Dir(from[paths.Data])
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "work", "tjek")
	res, err := moveData(from, folderLayout(target), target, "one", func(string) {})
	_ = os.Chmod(parent, 0o755)
	if err != nil {
		t.Fatalf("the move should go through, got %v", err)
	}
	if len(res.left) != 1 || !samePath(res.left[0], from[paths.Data]) {
		t.Errorf("left = %v, want the data folder", res.left)
	}
	if paths.Chosen() != target {
		t.Errorf("tjek reads %q, want the new place", paths.Chosen())
	}
	assertMilk(t)
	closeStore()

	cur, _ := currentLayout()
	res, err = moveData(cur, standard, "", "two", func(string) {})
	if err != nil {
		t.Fatalf("moving back over the leftover: %v", err)
	}
	if _, err := os.Stat(from[paths.Data] + ".old-two"); err != nil {
		t.Errorf("the leftover was not set aside: %v", err)
	}
	assertMilk(t)
}

func assertMilk(t *testing.T) {
	t.Helper()
	_, todos, err := loadForCLI()
	if err != nil {
		t.Fatal(err)
	}
	if len(todos) != 1 || todos[0].Title != "Buy milk" {
		t.Errorf("the store holds %v, want the one task", todos)
	}
}

// A move that cannot finish leaves everything where it was: a target that
// already holds tjek's files, or one inside the folder being moved.
func TestMoveDataRefusesAndLeavesThingsAlone(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	if code := runCLI([]string{"add", "Buy milk"}, false); code != 0 {
		t.Fatalf("add exited %d", code)
	}
	checkpointStore()
	closeStore()
	from, _ := currentLayout()

	occupied := filepath.Join(home, "occupied", "tjek")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "tasks.db"), []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := moveData(from, folderLayout(occupied), occupied, "x", func(string) {}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("a target holding a store should be refused, got %v", err)
	}
	inside := filepath.Join(from[paths.Data], "sub", "tjek")
	if _, err := moveData(from, folderLayout(inside), inside, "x", func(string) {}); err == nil {
		t.Error("a target inside the data folder should be refused")
	}

	if paths.Chosen() != "" {
		t.Error("a refused move must not record a new place")
	}
	if entries, _ := os.ReadDir(occupied); len(entries) != 1 {
		t.Errorf("the refused target was changed: %v", entries)
	}
	assertMilk(t)
}

// kindOfFile must file each config file where tjek reads it from.
func TestKindOfFileMatchesWhereFilesAreRead(t *testing.T) {
	setTestHome(t, t.TempDir())
	for _, p := range []string{settingsPath(), syncConfigPath(), sharedConfigPath()} {
		if k := kindOfFile(filepath.Base(p)); k != paths.Config {
			t.Errorf("%s files as kind %d, want config", filepath.Base(p), k)
		}
	}
	if k := kindOfFile(filepath.Base(dbPath())); k != paths.Data {
		t.Errorf("the store files as kind %d, want data", k)
	}
	if k := kindOfFile(filepath.Base(undoPersistPath())); k != paths.State {
		t.Errorf("the undo history files as kind %d, want state", k)
	}
}

// Settings → Data folder: the row opens the picker, a typed folder asks to
// move into a tjek folder inside it, and yes quits with the move left for
// Main; no leaves everything as it was.
func TestScriptDataFolderMove(t *testing.T) {
	m := modelWithTasks(t)
	m.tab = tabSettings
	folder := t.TempDir()

	m.settingsCursor = settingDataFolder
	m = typePath(t, sendKey(t, m, "enter"))
	if m.mode != modeEditDataFolder {
		t.Fatalf("enter on the data folder row left mode %v", m.mode)
	}
	m.textInput.SetValue(folder)
	m = sendKey(t, m, "enter")
	if m.mode != modeConfirm || !strings.Contains(m.confirmMsg, "tjek") {
		t.Fatalf("mode %v, prompt %q: want the move confirmed first", m.mode, m.confirmMsg)
	}
	m = sendKey(t, m, "n")
	if m.mode != modeNormal || m.moveDataTo != nil {
		t.Fatal("no should leave the data where it is")
	}

	m = typePath(t, sendKey(t, m, "enter"))
	m.textInput.SetValue(folder)
	m = sendKey(t, m, "enter")
	m, cmd := sendKeyCmd(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.moveDataTo == nil || m.moveDataTo.folder != filepath.Join(folder, "tjek") {
		t.Fatalf("move = %+v, want one into %s", m.moveDataTo, filepath.Join(folder, "tjek"))
	}
	if cmd == nil {
		t.Fatal("yes should quit so the move can run")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("yes should quit so the move can run")
	}
}

// A blank folder while nothing is chosen has nothing to move back; TJEK_HOME
// leaves the choice to the variable.
func TestDataFolderRowWithNothingToDo(t *testing.T) {
	m := modelWithTasks(t)
	m.tab = tabSettings
	m.settingsCursor = settingDataFolder
	m = typePath(t, sendKey(t, m, "enter"))
	m = sendKey(t, m, "enter")
	if m.mode != modeNormal || m.moveDataTo != nil || !strings.Contains(m.err, "standard place") {
		t.Errorf("mode %v, toast %q: a blank folder should say the data is already in the standard place", m.mode, m.err)
	}

	t.Setenv("TJEK_HOME", t.TempDir())
	m = sendKey(t, m, "enter")
	if m.mode == modePickPath || !strings.Contains(m.err, "TJEK_HOME") {
		t.Errorf("mode %v, toast %q: with TJEK_HOME set the row should say so instead of picking", m.mode, m.err)
	}
}
