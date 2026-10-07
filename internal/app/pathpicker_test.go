package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Iliorn/tjek/todo"
)

// loadPicker reads the picker's folder into it, as the command its last key
// returned would: tests drop commands (sendKey).
func loadPicker(t *testing.T, m model) model {
	t.Helper()
	if m.mode != modePickPath {
		t.Fatalf("mode = %v, want the picker", m.mode)
	}
	next, _ := m.Update(m.picker.fp.Init()())
	return next.(model)
}

// typePath leaves the picker for its typed prompt, emptied, as the tests that
// type a path at the prompt expect to find it.
func typePath(t *testing.T, m model) model {
	t.Helper()
	if m.mode != modePickPath {
		t.Fatalf("mode = %v, want the picker", m.mode)
	}
	m = sendKey(t, m, "/")
	m.textInput.SetValue("")
	return m
}

// pickerFolder is a folder holding a subfolder, an export and a stray file:
// the picker lists them as sub, backup.json, notes.txt.
func pickerFolder(t *testing.T) (dir, export string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	export = filepath.Join(dir, "backup.json")
	if err := writeExport(export, []todo.Todo{todo.New("From the backup")}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, export
}

// The import row opens the picker where the last one ended; enter on an
// export imports it, and the next picker opens in the folder it was in.
func TestPathPickerImportsTheMarkedExport(t *testing.T) {
	setTestHome(t, t.TempDir())
	testStore(t)
	m := initialModel(newSQLiteRepo())
	m.termWidth, m.termHeight = 120, 40
	m.tab = tabSettings
	dir, _ := pickerFolder(t)
	sub := filepath.Join(dir, "sub")
	export := filepath.Join(sub, "older.json")
	if err := writeExport(export, []todo.Todo{todo.New("From the backup")}); err != nil {
		t.Fatal(err)
	}
	m.pickerFolder = dir

	m = loadPicker(t, openSetting(t, m, settingImportFile))
	if got := m.picker.fp.CurrentDirectory; got != dir {
		t.Fatalf("opened in %s, want the last picker's folder %s", got, dir)
	}
	m = loadPicker(t, sendKey(t, m, "enter")) // into sub
	next, cmd := m.Update(keyMsgFor("enter"))
	m = next.(model)
	if cmd == nil || m.mode != modeNormal {
		t.Fatalf("enter on %s: mode %v (%s), want the import started", export, m.mode, m.err)
	}
	if _, ok := cmd().(importDoneMsg); !ok {
		t.Fatal("the import did not run")
	}
	if s, _ := loadSettings(); s.PickerFolder != sub {
		t.Errorf("settings keep picker folder %q, want %q", s.PickerFolder, sub)
	}
}

// A file of another type is refused where it is, without leaving the picker.
func TestPathPickerRefusesAnotherFileType(t *testing.T) {
	m := settingsModel(t)
	dir, _ := pickerFolder(t)
	m.pickerFolder = dir
	m = loadPicker(t, openSetting(t, m, settingImportFile))
	m = script(t, m, "down", "down", "enter") // notes.txt
	if m.mode != modePickPath || !strings.Contains(m.err, "notes.txt") {
		t.Errorf("enter on notes.txt: mode %v, error %q; want the picker kept and the file named", m.mode, m.err)
	}
}

// In a folder pick, enter picks the marked folder and space the one the
// picker is in.
func TestPathPickerPicksFolders(t *testing.T) {
	for _, c := range []struct {
		keys []string
		want func(dir string) string
	}{
		{[]string{"enter"}, func(dir string) string { return filepath.Join(dir, "sub") }},
		{[]string{" "}, func(dir string) string { return dir }},
	} {
		m := settingsModel(t)
		dir, _ := pickerFolder(t)
		m.pickerFolder = dir
		m = loadPicker(t, openSetting(t, m, settingExportFolder))
		m = script(t, m, c.keys...)
		if want := c.want(dir); m.mode != modeNormal || m.exportFolder != want {
			t.Errorf("%q: mode %v, export folder %q (%s); want %q", c.keys, m.mode, m.exportFolder, m.err, want)
		}
	}
}

// → opens a folder and ← goes back up; x turns the export off.
func TestPathPickerWalksFoldersAndTurnsExportOff(t *testing.T) {
	m := settingsModel(t)
	dir, _ := pickerFolder(t)
	m.pickerFolder, m.exportFolder = dir, dir
	m = loadPicker(t, openSetting(t, m, settingExportFolder))
	m = loadPicker(t, sendKey(t, m, "right"))
	if got := m.picker.fp.CurrentDirectory; got != filepath.Join(dir, "sub") {
		t.Fatalf("→ on sub: in %s", got)
	}
	m = loadPicker(t, sendKey(t, m, "left"))
	if got := m.picker.fp.CurrentDirectory; got != dir {
		t.Fatalf("← from sub: in %s, want %s", got, dir)
	}
	m = sendKey(t, m, "x")
	if m.mode != modeNormal || m.exportFolder != "" {
		t.Errorf("x: mode %v, export folder %q; want export off", m.mode, m.exportFolder)
	}
}

// A folder that cannot be read is stepped back out of, with the reason.
func TestPathPickerStepsBackOutOfAnUnreadableFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this user cannot read")
	}
	m := settingsModel(t)
	dir, _ := pickerFolder(t)
	locked := filepath.Join(dir, "sub")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	m.pickerFolder = dir
	m = loadPicker(t, openSetting(t, m, settingImportFile))
	m = sendKey(t, m, "right")
	if got := m.picker.fp.CurrentDirectory; got != dir || !strings.Contains(m.err, "sub") {
		t.Errorf("→ on an unreadable folder: in %s, error %q; want back in %s with the reason", got, m.err, dir)
	}
}

// / hands over to the typed prompt with the folder filled in; esc closes.
func TestPathPickerTypesOrCloses(t *testing.T) {
	m := settingsModel(t)
	dir, _ := pickerFolder(t)
	m.pickerFolder = dir
	m = loadPicker(t, openSetting(t, m, settingShareJoin))
	m = sendKey(t, m, "/")
	if want := dir + string(filepath.Separator); m.mode != modeShareJoin || m.textInput.Value() != want {
		t.Errorf("/: mode %v, prompt %q; want the join prompt holding %q", m.mode, m.textInput.Value(), want)
	}
	m = loadPicker(t, openSetting(t, sendKey(t, m, "esc"), settingShareJoin))
	if m = sendKey(t, m, "esc"); m.mode != modeNormal {
		t.Errorf("esc: mode %v, want the picker closed", m.mode)
	}
}

// With no last folder, or one that is gone, the picker opens on the Desktop,
// or in the home folder when there is none.
func TestPathPickerStartsOnTheDesktop(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	if got := pickerStart(filepath.Join(home, "gone")); got != home {
		t.Errorf("no Desktop: starts in %s, want home %s", got, home)
	}
	if runtime.GOOS == "windows" {
		return // the Desktop is the known folder's, not under the test home
	}
	desk := filepath.Join(home, "Desktop")
	if err := os.Mkdir(desk, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := pickerStart(""); got != desk {
		t.Errorf("starts in %s, want the Desktop %s", got, desk)
	}
}

// The picker fills the rows under the panes it takes, and no more, at any size.
func TestPathPickerFitsTheWindow(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {60, 16}} {
		m := settingsModel(t)
		dir, _ := pickerFolder(t)
		m.pickerFolder = dir
		m.termWidth, m.termHeight = size[0], size[1]
		m = loadPicker(t, openSetting(t, m, settingImportFile))
		out := m.View()
		if got := strings.Count(out, "\n") + 1; got != m.termHeight {
			t.Errorf("%dx%d: frame is %d lines", size[0], size[1], got)
		}
		for _, line := range strings.Split(out, "\n") {
			if ansi.StringWidth(line) > m.termWidth {
				t.Errorf("%dx%d: a line %d wide", size[0], size[1], ansi.StringWidth(line))
				break
			}
		}
		if !strings.Contains(out, "backup.json") {
			t.Errorf("%dx%d: the folder's export is not listed", size[0], size[1])
		}
	}
}
