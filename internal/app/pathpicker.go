package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// pathpicker.go browses for the file or folder a prompt asks for, so it need
// not be typed: the folder to share a project in, the .tjek file to join, the
// export folder and the export to import. It is bubbles' filepicker in tjek's
// colours, with the folder it is in as its heading. It opens where the last
// one ended (settings.json "picker_folder"), and the first time on the
// Desktop. What it picks goes through the typed prompt's own enter, so a
// pick is checked exactly as a typed path is; / leaves it for that prompt,
// to type a path instead.

// pathPick is what a picker is choosing.
type pathPick int

const (
	pickShareFolder pathPick = iota
	pickJoinFile
	pickExportFolder
	pickImportFile
)

// pathPickSpec is one kind of pick: whether it is a folder or a file of one
// of exts, the heading over the picker, and the typed prompt it hands to.
type pathPickSpec struct {
	folder      bool
	exts        []string
	title       string // translated; %s is the project's name
	mode        appMode
	placeholder string // translated; %s is the project's name
}

var pathPickSpecs = map[pathPick]pathPickSpec{
	pickShareFolder: {folder: true, title: "Folder to share '%s' in", mode: modeShareFolder,
		placeholder: "Folder to share '%s' in, e.g. in your OneDrive"},
	pickJoinFile: {exts: []string{".tjek", ".TJEK"}, title: "The .tjek file of a shared project", mode: modeShareJoin,
		placeholder: "The .tjek file of a shared project"},
	pickExportFolder: {folder: true, title: "Folder to keep tjek-export.json in", mode: modeEditExportFolder,
		placeholder: "Folder to keep tjek-export.json in (blank turns it off)"},
	pickImportFile: {exts: []string{".json", ".JSON"}, title: "A tjek export (.json)", mode: modeImportFile,
		placeholder: "Path to a tjek export (.json)"},
}

// pathPicker is an open picker.
type pathPicker struct {
	fp      filepicker.Model
	purpose pathPick
}

// sprintfName fills a spec string's %s with the project's name, where it has one.
func sprintfName(s, name string) string {
	if strings.Contains(s, "%s") {
		return fmt.Sprintf(s, name)
	}
	return s
}

// openPathPicker opens a picker for purpose.
func (m model) openPathPicker(purpose pathPick) (tea.Model, tea.Cmd) {
	spec := pathPickSpecs[purpose]
	fp := filepicker.New()
	fp.CurrentDirectory = pickerStart(m.pickerFolder)
	fp.ShowPermissions, fp.ShowSize = false, false
	fp.AutoHeight = false
	fp.SetHeight(m.pickerRows())
	fp.Cursor = "›"
	fp.FileAllowed = !spec.folder
	fp.AllowedTypes = spec.exts
	// esc leaves the picker rather than going up a folder; enter is taken
	// here in folder mode (updatePathPicker), so the list only opens folders.
	fp.KeyMap.Back = key.NewBinding(key.WithKeys("left", "h", "backspace"))
	fp.Styles = pickerStyles(spec.folder)
	m.picker = pathPicker{fp: fp, purpose: purpose}
	m.mode = modePickPath
	return m, fp.Init()
}

// pickerStart is the folder a picker opens in: where the last one ended, or
// the Desktop, or the home folder, whichever is there first.
func pickerStart(last string) string {
	for _, dir := range []string{last, desktopDir()} {
		if info, err := os.Stat(dir); dir != "" && err == nil && info.IsDir() {
			return dir
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

// pickerRows is how many entries the picker lists: a third of the terminal,
// within 5 to 12.
func (m model) pickerRows() int {
	return max(5, min(12, m.termHeight/3))
}

// pickerOverheadLines is the picker's height below the panes: its box (two
// borders, a blank row inside each, the heading and the rows) and the hint
// line under it.
func (m model) pickerOverheadLines() int {
	return 4 + 1 + m.pickerRows() + 1
}

func pickerStyles(folder bool) filepicker.Styles {
	s := filepicker.DefaultStyles()
	s.Cursor = selectedStyle
	s.Selected = selectedStyle
	s.DisabledCursor = dimStyle
	s.DisabledSelected = dimStyle
	s.Directory = lipgloss.NewStyle().Foreground(currentTheme.blue)
	s.Symlink = lipgloss.NewStyle().Foreground(currentTheme.teal)
	s.File = normalStyle
	if folder {
		// Files are only there to show what a folder holds.
		s.File = dimStyle
	}
	s.DisabledFile = dimStyle
	s.EmptyDirectory = dimStyle.PaddingLeft(2).SetString(tr("This folder is empty"))
	return s
}

// updatePathPicker moves through the folders and picks. In a folder pick,
// enter picks the marked folder and space the one the picker is in; in a file
// pick, enter picks the marked file. / hands over to the typed prompt, esc
// closes.
func (m model) updatePathPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	spec := pathPickSpecs[m.picker.purpose]
	fp := m.picker.fp
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			m.mode = modeNormal
			return m, nil
		case "/":
			return m.typePickedPath(fp.CurrentDirectory+string(filepath.Separator), "")
		case "x":
			// The export folder's prompt reads a blank as "turn it off".
			if m.picker.purpose == pickExportFolder {
				m.mode = spec.mode
				m.textInput.SetValue("")
				return m.updateForMode(tea.KeyMsg{Type: tea.KeyEnter})
			}
		case " ":
			if spec.folder {
				return m.typePickedPath(fp.CurrentDirectory, fp.CurrentDirectory)
			}
		case "enter":
			if spec.folder {
				// Open the marked folder; having opened it is having picked it.
				from := fp.CurrentDirectory
				next, cmd := m.openPickerEntry(k)
				if n := next.(model); n.picker.fp.CurrentDirectory != from && n.mode == modePickPath {
					return n.typePickedPath(n.picker.fp.CurrentDirectory, from)
				}
				return next, cmd
			}
		}
		if key.Matches(k, fp.KeyMap.Open) {
			next, cmd := m.openPickerEntry(k)
			n := next.(model)
			if ok, path := n.picker.fp.DidSelectFile(k); ok {
				return n.typePickedPath(path, filepath.Dir(path))
			}
			if ok, path := n.picker.fp.DidSelectDisabledFile(k); ok {
				n.flashError(fmt.Sprintf(tr("Not a %s file: %s"), spec.exts[0], filepath.Base(path)))
				return n, clearErrAfter()
			}
			return n, cmd
		}
	}
	var cmd tea.Cmd
	m.picker.fp, cmd = fp.Update(msg)
	return m, cmd
}

// openPickerEntry hands an open key to the list. A folder that cannot be read
// is stepped back out of at once: the list would otherwise go on showing the
// folder it left under the new one's name, where a pick would name a file
// that is not there.
func (m model) openPickerEntry(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	from := m.picker.fp.CurrentDirectory
	fp, cmd := m.picker.fp.Update(k)
	if to := fp.CurrentDirectory; to != from {
		if err := readable(to); err != nil {
			fp, cmd = fp.Update(tea.KeyMsg{Type: tea.KeyLeft})
			m.picker.fp = fp
			m.flashError(fmt.Sprintf(tr("Can't open %s"), filepath.Base(to)))
			return m, tea.Batch(cmd, clearErrAfter())
		}
	}
	m.picker.fp = fp
	return m, cmd
}

// readable reports whether a folder's entries can be listed.
func readable(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.ReadDir(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// typePickedPath leaves the picker for its typed prompt with path filled in.
// With ended set, the path was picked: the next picker opens in ended, and the
// prompt's enter takes the path, checking it as it checks a typed one.
// Without, the prompt waits for the rest of a path to be typed.
func (m model) typePickedPath(path, ended string) (tea.Model, tea.Cmd) {
	spec := pathPickSpecs[m.picker.purpose]
	m.mode = spec.mode
	m.textInput.SetValue(path)
	m.textInput.CursorEnd()
	m.textInput.Placeholder = sprintfName(tr(spec.placeholder), m.pendingProjectName)
	m.textInput.Focus()
	if ended == "" {
		return m, nil
	}
	if ended != m.pickerFolder {
		m.pickerFolder = ended
		m.persistSettings()
	}
	return m.updateForMode(tea.KeyMsg{Type: tea.KeyEnter})
}

// renderPathPicker draws the picker under the panes: a box titled with what
// is being chosen, holding the folder it is in and that folder's entries,
// and the keys under it.
func (m model) renderPathPicker(w int) string {
	spec := pathPickSpecs[m.picker.purpose]
	inner := max(1, w-2) // the box's padding
	rows := m.pickerRows()
	lines := strings.Split(strings.TrimRight(m.picker.fp.View(), "\n"), "\n")
	body := make([]string, 0, rows+1)
	body = append(body, headerStyle.Render(truncateLeft(exportFolderDisplay(m.picker.fp.CurrentDirectory), inner)))
	for i := range rows {
		line := ""
		if i < len(lines) {
			line = ansi.Truncate(lines[i], inner, "…")
		}
		body = append(body, line)
	}
	// A blank row above and below the list, as the panes have.
	box := inputStyle.Padding(1, 1).Width(w).Render(strings.Join(body, "\n"))
	box = withBorderTitle(box, sprintfName(tr(spec.title), m.pendingProjectName), w, true)
	hint := tr("↑/↓ move · → open · ← up · enter picks the file · / type a path · esc cancels")
	switch {
	case m.picker.purpose == pickExportFolder:
		hint = tr("↑/↓ move · → open · ← up · enter picks the marked folder · space this one · x turns export off · / type a path · esc cancels")
	case spec.folder:
		hint = tr("↑/↓ move · → open · ← up · enter picks the marked folder · space this one · / type a path · esc cancels")
	}
	return box + "\n" + helpStyle.Render("    "+truncate(hint, w))
}

// truncateLeft shortens a path from its start, so the end of it, the folder
// the picker is in, stays in view.
func truncateLeft(s string, w int) string {
	r := []rune(s)
	if ansi.StringWidth(s) <= w || w < 2 {
		return s
	}
	for len(r) > 0 && ansi.StringWidth(string(r))+1 > w {
		r = r[1:]
	}
	return "…" + string(r)
}
