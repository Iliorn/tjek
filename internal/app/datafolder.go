package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Iliorn/tjek/paths"
)

// datafolder.go moves everything tjek keeps into a folder chosen in Settings
// (Settings → Data folder), or back to the standard places. Someone whose
// agent may write only inside one folder can keep tjek there without setting
// TJEK_HOME by hand.
//
// The move runs after the app has quit, with the store closed: Windows will
// not rename a folder holding an open file, and a copy taken under a live
// connection could miss a write. Main runs finishDataMove and starts tjek
// again in the new place.
//
// It copies, checks the copied store, sets the files aside as
// <folder>.moved-<time>, and only then records the new place
// (paths.SetChosen). A failure at any step undoes the steps before it, so
// tjek is either wholly in the old place or wholly in the new one.

// dataMove is a move the app has asked Main to make on its way out: into
// folder, or with folder "", back to the standard places.
type dataMove struct{ folder string }

// dataKinds are the directories a move carries. The cache is left behind: it
// holds only the notes open in an editor, and is made again on demand.
var dataKinds = []paths.Kind{paths.Config, paths.Data, paths.State}

// dataLayout is where each kind lives.
type dataLayout map[paths.Kind]string

func currentLayout() (dataLayout, error) { return layoutBy(paths.Dir) }

func standardLayout() (dataLayout, error) { return layoutBy(paths.Standard) }

func layoutBy(dir func(paths.Kind) (string, error)) (dataLayout, error) {
	l := dataLayout{}
	for _, k := range dataKinds {
		d, err := dir(k)
		if err != nil {
			return nil, err
		}
		l[k] = d
	}
	return l, nil
}

// folderLayout keeps every kind in dir.
func folderLayout(dir string) dataLayout {
	l := dataLayout{}
	for _, k := range dataKinds {
		l[k] = dir
	}
	return l
}

// dirs is the layout's distinct directories, in kind order.
func (l dataLayout) dirs() []string {
	var out []string
	for _, k := range dataKinds {
		if d := l[k]; d != "" && !containsPath(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// configFiles are the names tjek keeps in the config directory; a move into
// the standard places files them there, and everything else by kindOfFile.
var configFiles = map[string]bool{"settings.json": true, "sync.json": true, "shared.json": true}

// kindOfFile is the directory a file belongs in by its name: the store and
// its sidecars and backups are data, the settings config, the rest state.
func kindOfFile(name string) paths.Kind {
	switch {
	case configFiles[name]:
		return paths.Config
	case strings.HasPrefix(name, "tasks."):
		return paths.Data
	}
	return paths.State
}

// carried reports whether a move takes name along. The location file stays:
// it is what says where tjek is. The notes folder is the cache's.
func carried(name string) bool {
	return name != paths.LocationFile && name != paths.LocationFile+".tmp" && name != "notes"
}

// moveResult is what a move did besides moving: the folders it set aside,
// and the ones Windows would not let it rename, which are left as they were.
type moveResult struct {
	aside []string
	left  []string
}

// moveData moves what from holds into to, sets the old folders aside with
// stamp, and records chosen as the new place ("" for the standard places).
// say reports each step as it starts. On an error nothing has moved.
//
// Setting an old folder aside is tidiness, not safety: once the copy is
// checked, the data is safe in its new place. So a folder Windows will not
// rename (a file in it still open somewhere) is left where it was, rather
// than undoing a move that worked.
func moveData(from, to dataLayout, chosen, stamp string, say func(string)) (moveResult, error) {
	var res moveResult
	sources, targets := from.dirs(), to.dirs()
	for _, src := range sources {
		for _, dst := range targets {
			if samePath(src, dst) {
				return res, fmt.Errorf("tjek's data is already in %s", dst)
			}
			if within(dst, src) || within(src, dst) {
				return res, fmt.Errorf("%s and %s are inside one another", src, dst)
			}
		}
	}

	type item struct{ src, dst string }
	var items []item
	var store string
	var stale []string // standard folders holding an earlier copy, moving back
	for _, src := range sources {
		entries, err := os.ReadDir(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return res, err
		}
		for _, e := range entries {
			if !carried(e.Name()) {
				continue
			}
			dst := filepath.Join(to[kindOfFile(e.Name())], e.Name())
			if _, err := os.Lstat(dst); err == nil {
				// The standard places are tjek's own, and what is in them now
				// is an earlier copy a move could not set aside: it goes aside
				// now. A folder someone chose may hold anything; it is refused.
				if chosen != "" {
					return res, fmt.Errorf("%s already exists; choose an empty folder", dst)
				}
				if dir := filepath.Dir(dst); !containsPath(stale, dir) {
					stale = append(stale, dir)
				}
			}
			items = append(items, item{filepath.Join(src, e.Name()), dst})
			if e.Name() == "tasks.db" {
				store = dst
			}
		}
	}

	var staleAside []string
	undoStale := func() {
		for i := len(staleAside) - 1; i >= 0; i-- {
			_ = os.Rename(staleAside[i], strings.TrimSuffix(staleAside[i], ".old-"+stamp))
		}
	}
	for _, dir := range stale {
		to := dir + ".old-" + stamp
		if err := renameRetrying(dir, to); err != nil {
			undoStale()
			return res, fmt.Errorf("could not set the earlier copy in %s aside (is tjek open in another window?): %w", dir, err)
		}
		staleAside = append(staleAside, to)
	}

	var copied []string
	undoCopies := func() {
		for _, d := range copied {
			_ = os.RemoveAll(d)
		}
		undoStale()
	}
	say(fmt.Sprintf(tr("Copying to %s…"), strings.Join(targets, ", ")))
	for _, it := range items {
		if err := os.MkdirAll(filepath.Dir(it.dst), 0o755); err != nil {
			undoCopies()
			return res, err
		}
		copied = append(copied, it.dst)
		if err := copyTree(it.src, it.dst); err != nil {
			undoCopies()
			return res, fmt.Errorf("copying %s: %w", it.src, err)
		}
	}
	if store != "" {
		say(tr("Checking the copy…"))
		if err := checkStore(store); err != nil {
			undoCopies()
			return res, fmt.Errorf("the copied task store does not read back: %w", err)
		}
	}
	// The old folders go aside before the new place is recorded: the record
	// lives in the standard config folder, which may be one of them.
	say(tr("Setting the old folders aside…"))
	var aside []string
	for _, src := range sources {
		if _, err := os.Stat(src); err != nil {
			continue
		}
		to := src + ".moved-" + stamp
		if err := renameRetrying(src, to); err != nil {
			res.left = append(res.left, src)
			continue
		}
		aside = append(aside, to)
	}
	if err := paths.SetChosen(chosen); err != nil {
		for i := len(aside) - 1; i >= 0; i-- {
			_ = os.Rename(aside[i], strings.TrimSuffix(aside[i], ".moved-"+stamp))
		}
		undoCopies()
		return moveResult{}, fmt.Errorf("could not record the new place: %w", err)
	}
	res.aside = append(staleAside, aside...)
	return res, nil
}

// renameTries and renamePause bound how long a rename waits out a file held
// open: Windows refuses to rename a folder while anything has a file in it
// open, and a virus scanner or the search indexer lets go within moments.
var (
	renameTries = 10
	renamePause = 200 * time.Millisecond
)

func renameRetrying(from, to string) error {
	var err error
	for i := 0; i < renameTries; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(renamePause)
	}
	return err
}

// checkStore opens the store at path read-only and has SQLite check it.
func checkStore(path string) error {
	h, err := openStoreFileAt(path, true)
	if err != nil {
		return err
	}
	defer h.Close()
	var result string
	if err := h.QueryRow(`PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New(result)
	}
	return nil
}

// copyTree copies a file, or a folder and everything in it, keeping modes.
// Anything that is neither (a link, a socket) is skipped.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyDataFile(path, target, info.Mode().Perm())
		}
		return nil
	})
}

// copyDataFile copies one file to a new dst, never over an existing one, and
// syncs it, since the original is set aside next.
func copyDataFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || runtime.GOOS == "windows" && strings.EqualFold(a, b)
}

// within reports whether path is inside dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func containsPath(list []string, p string) bool {
	for _, q := range list {
		if samePath(q, p) {
			return true
		}
	}
	return false
}

// finishDataMove makes the move the app asked for, once it has quit, saying
// each step as it goes, so that if one hangs the screen says which. It
// returns whether the data moved.
func finishDataMove(mv dataMove) bool {
	say := func(line string) { fmt.Println(line) }
	closeStore()
	from, err := currentLayout()
	if err == nil {
		to := folderLayout(mv.folder)
		if mv.folder == "" {
			to, err = standardLayout()
		}
		if err == nil {
			var res moveResult
			if res, err = moveData(from, to, mv.folder, time.Now().Format("20060102-150405"), say); err == nil {
				where := mv.folder
				if where == "" {
					where = to[paths.Data]
				}
				say(fmt.Sprintf(tr("Moved tjek's data to %s."), where))
				if len(res.aside) > 0 {
					say(fmt.Sprintf(tr("The old folders are kept as %s; delete them when all is well."), strings.Join(res.aside, ", ")))
				}
				if len(res.left) > 0 {
					say(fmt.Sprintf(tr("%s could not be renamed and is left as it was; tjek no longer reads it, so delete it when all is well."), strings.Join(res.left, ", ")))
				}
				return true
			}
		}
	}
	fmt.Fprintf(os.Stderr, tr("tjek: the data was not moved: %v"), err)
	fmt.Fprintln(os.Stderr)
	return false
}

// ── The Settings row ─────────────────────────────────────────────────────────

// dataFolderDisplay is the Settings row's value: where tjek keeps its tasks.
func dataFolderDisplay() string {
	if paths.HomeOverride() != "" {
		return exportFolderDisplay(paths.HomeOverride()) + " (TJEK_HOME)"
	}
	if dir := paths.Chosen(); dir != "" {
		return exportFolderDisplay(dir)
	}
	return tr("standard place")
}

func (m model) openDataFolderEditor() (tea.Model, tea.Cmd) {
	if paths.HomeOverride() != "" {
		m.flashError(tr("TJEK_HOME sets the folder; unset it to choose one here"))
		return m, clearErrAfter()
	}
	return m.openPathPicker(pickDataFolder)
}

// updateEditDataFolder takes the folder to move tjek into: tab completes a
// folder name, enter asks to move (blank: back to the standard places), esc
// leaves it as it was. tjek goes into a "tjek" folder inside the one named,
// so a folder that holds other things can be chosen.
func (m model) updateEditDataFolder(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "tab":
			m.textInput.SetValue(completePath(m.textInput.Value(), true))
			m.textInput.CursorEnd()
			return m, nil
		case "enter":
			typed := strings.TrimSpace(m.textInput.Value())
			if typed == "" {
				m.mode = modeNormal
				if paths.Chosen() == "" {
					m.flashInfo(tr("tjek's data is already in the standard place"))
					return m, clearErrAfter()
				}
				return m.askDataMove("")
			}
			folder, err := filepath.Abs(expandHome(typed))
			if info, statErr := os.Stat(folder); err != nil || statErr != nil || !info.IsDir() {
				m.flashError(fmt.Sprintf(tr("Not a folder: %s"), typed))
				return m, clearErrAfter()
			}
			target := filepath.Join(folder, paths.AppDirName)
			if dir, err := paths.Dir(paths.Data); err == nil && samePath(dir, target) {
				m.mode = modeNormal
				m.flashInfo(fmt.Sprintf(tr("tjek's data is already in %s"), exportFolderDisplay(target)))
				return m, clearErrAfter()
			}
			return m.askDataMove(target)
		case "esc":
			m.mode = modeNormal
			return m, nil
		}
	}
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

// askDataMove confirms a move into folder ("" for the standard places).
func (m model) askDataMove(folder string) (tea.Model, tea.Cmd) {
	m.pendingDataFolder = folder
	where := tr("the standard place")
	if folder != "" {
		where = exportFolderDisplay(folder)
	}
	m.confirmMsg = fmt.Sprintf(tr("Move tjek's data to %s? tjek closes to do it; close any other tjek first. (y/n)"), where)
	m.confirmOnYes = (*model).confirmDataMove
	m.mode = modeConfirm
	return m, nil
}

// confirmDataMove quits as q does, also stopping the in-process server and
// the live sync, and leaves the move for Main (finishDataMove).
func (m *model) confirmDataMove() tea.Cmd {
	m.flushPendingWrites()
	m.closeWatcher()
	if m.inprocServer != nil {
		_ = m.inprocServer.Close()
		m.inprocServer = nil
		if m.inprocStop != nil {
			m.inprocStop()
			m.inprocStop = nil
		}
	}
	if m.liveSync != nil {
		m.liveSync.Close()
		m.liveSync = nil
	}
	m.moveDataTo = &dataMove{folder: m.pendingDataFolder}
	return tea.Quit
}
