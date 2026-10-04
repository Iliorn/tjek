package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Iliorn/tjek/hlc"
	"github.com/Iliorn/tjek/paths"
	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/tasksync"
	"github.com/Iliorn/tjek/todo"
)

// sharedproject.go shares one project with other people through one file they
// can all reach, in a OneDrive or Dropbox folder most often: Trip.tjek holds
// the project's name and ID and every task of it, tombstones, stamps and
// history included. There is no server.
//
// Every device reads the file and writes it. A pass folds the file's tasks
// into the store with the sync merge (mergeIntoStore), then writes the
// project as the store now holds it back to the file, when that differs from
// what the file says. The merge is a CRDT (tasksync.Merge), which is what
// makes one file shared by several writers safe:
//
//   - Two devices writing at once: the cloud service keeps both, one as a
//     conflict copy beside the file ("Trip (Mark's conflicted copy).tjek").
//     A pass reads the copies with the file, merges them all, writes the
//     result and removes the copies (sharedConflictCopies).
//   - A service that lets the later write replace the earlier instead: the
//     device whose write was lost still holds its change in its store, sees
//     the file lacks it on its next pass, and writes it again.
//
// Either way a change can arrive late but not be lost, and every device ends
// with the same project.
//
// Which projects this device shares, and in which file, is local:
// shared.json in the config directory.
//
// A shared project lives on the devices that joined its file and nowhere
// else: the sync server neither gets nor gives its tasks (keepsOutOfSync), so
// each machine joins by itself, and leaving on one touches no other.
// Leaving removes the project's tasks from the device outright, with no
// tombstones: a tombstone would travel back into the file on a later join
// and delete the tasks for everyone. The removed IDs are remembered until
// then, so a sync server that still holds the tasks cannot bring them back.

const (
	sharedExt = ".tjek"
	// sharedFormat is the version of the file's shape. A file from a newer
	// format is refused rather than half-read.
	sharedFormat = 1
)

// sharedFile is a shared project's file.
type sharedFile struct {
	Format  int         `json:"format"`
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Created time.Time   `json:"created"`
	Tasks   []todo.Todo `json:"tasks"`
	// Departed is the tasks moved out of the project, each with the stamp of
	// the move (see departures).
	Departed map[string]hlc.Stamp `json:"departed,omitempty"`
	// Renamed is when Name was set by a rename, and Former the names the
	// project had before (see resolveSharedName).
	Renamed time.Time `json:"renamed,omitzero"`
	Former  []string  `json:"former,omitempty"`
}

// sharedProject is one project this device shares.
type sharedProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	File string `json:"file"`
	// Renamed and Former are this device's view of the project's name, as
	// the file's (sharedFile).
	Renamed time.Time `json:"renamed,omitzero"`
	Former  []string  `json:"former,omitempty"`
	// Moving is the names this device is moving the project's tasks off, to
	// Name, while it does: set before the move and cleared after, so a task
	// is never under a name shared.json does not tie to the project, and a
	// move cut short is finished by the next pass (finishMoving).
	Moving []string `json:"moving,omitempty"`
}

// sharedLeft is a project this device left: the tasks it removed, which a
// sync must not bring back until the project is joined again.
type sharedLeft struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Tasks []string `json:"tasks"`
}

// sharedConfig is shared.json.
type sharedConfig struct {
	Projects []sharedProject `json:"projects,omitempty"`
	Left     []sharedLeft    `json:"left,omitempty"`
}

func sharedConfigPath() string { return paths.For(paths.Config, "shared.json") }

// loadSharedConfig reads shared.json; a missing file is no shared projects.
func loadSharedConfig() (sharedConfig, error) {
	var c sharedConfig
	data, err := os.ReadFile(sharedConfigPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, err
	default:
		if err := json.Unmarshal(data, &c); err != nil {
			return c, fmt.Errorf("%s: %w", sharedConfigPath(), err)
		}
	}
	return c, nil
}

func saveSharedConfig(c sharedConfig) error {
	if _, err := paths.Ensure(paths.Config); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(sharedConfigPath(), append(data, '\n'), 0o600)
}

// clone is a copy the share, join and leave edits can change without
// touching c, so a failed save leaves the loaded list as it was.
func (c sharedConfig) clone() sharedConfig {
	c.Projects = slices.Clone(c.Projects)
	c.Left = slices.Clone(c.Left)
	return c
}

// keepsOutOfSync reports whether the sync server must neither get t nor
// give it: a task of a project shared here, which travels through its file,
// or one this device removed by leaving a project. A name the project's
// tasks are being moved off counts too (Moving). A name it had before a
// rename does not once they are moved, so a project of one's own can take it.
func (c sharedConfig) keepsOutOfSync(t *todo.Todo) bool {
	if t.Project != "" {
		for _, p := range c.Projects {
			if p.Name == t.Project || slices.Contains(p.Moving, t.Project) {
				return true
			}
		}
	}
	for _, l := range c.Left {
		if slices.Contains(l.Tasks, t.ID) {
			return true
		}
	}
	return false
}

// withoutShared is tasks less the ones keepsOutOfSync keeps from the sync
// server. The slice is new; tasks is left as it was.
func (c sharedConfig) withoutShared(tasks []todo.Todo) []todo.Todo {
	if len(c.Projects) == 0 && len(c.Left) == 0 {
		return tasks
	}
	out := make([]todo.Todo, 0, len(tasks))
	for i := range tasks {
		if !c.keepsOutOfSync(&tasks[i]) {
			out = append(out, tasks[i])
		}
	}
	return out
}

// timerScope says whose timers on a task this device runs. On a task of a
// project shared here, only the ones me started: the others' timers are
// theirs, and starting one's own, stopping, or recovering one left running
// must not end their work for them. On any other task, every timer, since
// they are one person's on all of that person's devices.
type timerScope struct {
	me     string
	shared map[string]bool
}

func (c sharedConfig) timerScope(me string) timerScope {
	s := timerScope{me: me, shared: make(map[string]bool, len(c.Projects))}
	for _, p := range c.Projects {
		s.shared[p.Name] = true
	}
	return s
}

// owner is the who of t's timer methods (todo.TimeEntry.StartedBy).
func (s timerScope) owner(t *todo.Todo) string {
	return s.ownerIn(t.Project)
}

func (s timerScope) ownerIn(project string) string {
	if project != "" && s.shared[project] {
		return s.me
	}
	return ""
}

// set replaces the project with p's ID by p.
func (c *sharedConfig) set(p sharedProject) {
	for i := range c.Projects {
		if c.Projects[i].ID == p.ID {
			c.Projects[i] = p
		}
	}
}

// find is the shared project named name, if this device shares one.
func (c sharedConfig) find(name string) (sharedProject, bool) {
	for _, p := range c.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return sharedProject{}, false
}

// readSharedFile reads a project's file. It may be the project's own file or
// a conflict copy of it; either way a file from a newer format is refused.
func readSharedFile(path string) (sharedFile, error) {
	var f sharedFile
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return f, fmt.Errorf("%s is gone", path)
		}
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("%s is not a shared project: %w", filepath.Base(path), err)
	}
	if f.ID == "" {
		return f, fmt.Errorf("%s is not a shared project", filepath.Base(path))
	}
	if f.Format > sharedFormat {
		return f, fmt.Errorf("%s was written by a newer tjek; update to keep sharing it", filepath.Base(path))
	}
	return f, nil
}

// sharedFileName is the file a project is shared in: its name, with the
// characters a file name cannot hold on some system replaced.
func sharedFileName(project string) string {
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < ' ' {
			return '-'
		}
		return r
	}, project)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "project"
	}
	return name + sharedExt
}

// sharedTarget resolves a path as typed, a folder or a file, and says which.
func sharedTarget(typed string) (path string, isDir bool, err error) {
	path, err = filepath.Abs(expandHome(strings.TrimSpace(typed)))
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("no such file or folder: %s", typed)
	}
	return path, info.IsDir(), nil
}

// startSharing shares project name in a new file in folder, and records it
// here. A folder that already holds the project's file joins it instead.
func startSharing(c *sharedConfig, name, typedFolder string) (sharedProject, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return sharedProject{}, errors.New("no project named")
	}
	if p, ok := c.find(name); ok {
		return p, fmt.Errorf("%q is already shared in %s", p.Name, p.File)
	}
	folder, isDir, err := sharedTarget(typedFolder)
	if err != nil {
		return sharedProject{}, err
	}
	if !isDir {
		return sharedProject{}, fmt.Errorf("not a folder: %s", typedFolder)
	}
	path := filepath.Join(folder, sharedFileName(name))
	if f, err := readSharedFile(path); err == nil {
		if f.Name != name {
			return sharedProject{}, fmt.Errorf("%s already shares the project %q", path, f.Name)
		}
		return joinShared(c, path)
	} else if _, statErr := os.Stat(path); statErr == nil {
		return sharedProject{}, fmt.Errorf("%s is in the way: %w", path, err)
	}
	data, err := encodeSharedFile(sharedFile{
		Format: sharedFormat, ID: uuid.NewString(), Name: name, Created: time.Now(), Tasks: []todo.Todo{},
	})
	if err != nil {
		return sharedProject{}, err
	}
	if err := writeFileAtomic(path, data, 0o644); err != nil {
		return sharedProject{}, err
	}
	return joinShared(c, path)
}

// joinShared records the project a file holds as shared here. The path may
// also be a folder holding one shared project's file.
func joinShared(c *sharedConfig, typed string) (sharedProject, error) {
	path, isDir, err := sharedTarget(typed)
	if err != nil {
		return sharedProject{}, err
	}
	if isDir {
		files := sharedFilesIn(path)
		if len(files) != 1 {
			return sharedProject{}, fmt.Errorf("%s holds %d shared project files; name the one to join", typed, len(files))
		}
		path = files[0]
	}
	f, err := readSharedFile(path)
	if err != nil {
		return sharedProject{}, err
	}
	for _, p := range c.Projects {
		if p.ID == f.ID {
			return p, fmt.Errorf("already joined %q", p.Name)
		}
	}
	if p, ok := c.find(f.Name); ok {
		return p, fmt.Errorf("a shared project named %q is already joined from %s", p.Name, p.File)
	}
	p := sharedProject{ID: f.ID, Name: f.Name, File: path, Renamed: f.Renamed, Former: f.Former}
	c.Projects = append(c.Projects, p)
	// Joining again lets the file bring back what leaving removed.
	c.Left = slices.DeleteFunc(c.Left, func(l sharedLeft) bool { return l.ID == f.ID })
	return p, nil
}

// leaveShared stops sharing the project named name here and removes its
// tasks from this device. It first brings the file up to date, so nothing
// made here is lost to the others; a file it cannot reach does not stop the
// leave. The removal is outright, with no tombstones (see the top of the
// file), and returns the tasks that went.
func leaveShared(h *sql.DB, c *sharedConfig, name string, b rank.Biases, by editor) (sharedProject, []string, error) {
	p, ok := c.find(name)
	if !ok {
		return p, nil, fmt.Errorf("%q is not a shared project", name)
	}
	// A rename the pass adopts moves the tasks to the new name.
	if res, err := syncShared(h, p, b, by, nil); err == nil {
		p = res.project
	}
	ids, err := removeProjectTasks(h, p.Name)
	if err != nil {
		return p, nil, err
	}
	forgetUndoOf(ids)
	if _, err := detachOrphans(h, ids, b, by); err != nil {
		return p, nil, err
	}
	c.Projects = slices.DeleteFunc(c.Projects, func(x sharedProject) bool { return x.ID == p.ID })
	c.Left = append(c.Left, sharedLeft{ID: p.ID, Name: p.Name, Tasks: ids})
	return p, ids, nil
}

// forgetUndoOf takes the tasks ids, removed outright, out of the undo history
// `tjek undo` and the next start read (undoWithout). Failing to is no reason
// to fail what removed them, so an error is dropped, as a delete drops one.
func forgetUndoOf(ids []string) {
	if len(ids) == 0 {
		return
	}
	gone := make(map[string]bool, len(ids))
	for _, id := range ids {
		gone[id] = true
	}
	_ = editPersistedUndo(func(e []undoEntry) []undoEntry { return undoWithout(e, gone) })
}

// renameUndoProject moves the undo history `tjek undo` and the next start
// read from project old to name (undoRenamingProject).
func renameUndoProject(old, name string) {
	_ = editPersistedUndo(func(e []undoEntry) []undoEntry { return undoRenamingProject(e, old, name) })
}

// removeProjectTasks deletes every row of the project's tasks, tombstones
// included, with their children, history and dependency links, and returns
// their IDs. Nothing marks them deleted: to the store they were never here.
func removeProjectTasks(h *sql.DB, name string) ([]string, error) {
	tx, err := h.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM todos WHERE project = ?`, name)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := removeTaskRows(tx, ids); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	noteForeignWrite()
	return ids, nil
}

// removeTaskRows deletes every row of the tasks ids, as removeProjectTasks
// describes.
func removeTaskRows(tx *sql.Tx, ids []string) error {
	for _, id := range ids {
		for _, q := range []string{
			`DELETE FROM task_tags WHERE task_id = ?1`,
			`DELETE FROM task_dependencies WHERE task_id = ?1 OR depends_on_id = ?1`,
			`DELETE FROM task_comments WHERE task_id = ?1`,
			`DELETE FROM task_time_entries WHERE task_id = ?1`,
			`DELETE FROM task_events WHERE task_id = ?1`,
			`DELETE FROM todos WHERE id = ?1`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// localProjectTasks counts the live tasks this device already files under
// name: what joining a project of that name would hand to everyone in it.
func localProjectTasks(todos []todo.Todo, name string) int {
	n := 0
	for i := range todos {
		if !todos[i].Deleted && todos[i].Project == name {
			n++
		}
	}
	return n
}

// sharedConflictCopies is the copies a cloud service made of the project's
// file when two devices wrote it at once. Each names it differently
// ("Trip-LAPTOP.tjek", "Trip (Mark's conflicted copy 2026-09-29).tjek",
// "Trip (1).tjek"), but all keep the name and the extension around what they
// add; a file of another project that happens to match is left alone by the
// ID check in syncShared.
func sharedConflictCopies(file string) []string {
	dir, base := filepath.Split(file)
	stem := strings.TrimSuffix(base, sharedExt)
	var out []string
	for _, m := range sharedFilesIn(dir) {
		name := strings.TrimSuffix(filepath.Base(m), sharedExt)
		if name == stem || !strings.HasPrefix(name, stem) {
			continue
		}
		if next := name[len(stem)]; next == ' ' || next == '-' || next == '_' || next == '(' {
			out = append(out, m)
		}
	}
	return out
}

// sharedFilesIn is the shared project files in dir, in name order. It lists
// the folder rather than globbing it, since a folder's name may hold "[" or
// "*", which a glob would read as a pattern.
func sharedFilesIn(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), sharedExt) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// sharedResult is what one pass over a shared project did.
type sharedResult struct {
	changed bool // the merge changed the store
	wrote   bool // the file was rewritten
	// project is the project as the pass left it; renamedFrom is the name
	// it had here before, when the pass took up a rename made elsewhere.
	project     sharedProject
	renamedFrom string
	// movedAside is the name this device's own project of the new name
	// moved to, when the rename took its name (moveOwnProjectAside).
	movedAside string
	// removed is the tasks taken out of the store because they left the
	// project on another device.
	removed []string
}

// syncShared runs one pass over p's file: fold the file and any conflict
// copies of it into the store, then write the project back when the file
// does not already say what the store holds, and remove the copies it took.
//
// When the file names the project differently, the later rename wins
// (resolveSharedName). A rename made elsewhere is taken up here: this
// device's own tasks already under the new name move aside first
// (moveOwnProjectAside), adopt records the project under its new name with
// the old one as Moving (in shared.json), the project's tasks move to it, and
// adopt clears Moving. A task the file holds under a former name, from a
// device that had not yet heard of a rename, moves to the name the same way.
// adopt may be nil.
//
// A subtask whose parent is not in the project leaves the parent here
// (detachStraySubtasks): the others would get a subtask of a task they do not
// have.
func syncShared(h *sql.DB, p sharedProject, b rank.Biases, by editor, adopt func(sharedProject) error) (sharedResult, error) {
	if adopt == nil {
		adopt = func(sharedProject) error { return nil }
	}
	res := sharedResult{project: p}
	if len(p.Moving) > 0 {
		var err error
		if p, err = finishMoving(h, p, b, by, adopt); err != nil {
			return res, err
		}
		res.project = p
	}
	f, err := readSharedFile(p.File)
	if err != nil {
		return res, err
	}
	if f.ID != p.ID {
		return res, fmt.Errorf("%s now holds another project (%q)", p.File, f.Name)
	}
	incoming := f.Tasks
	departed := maps.Clone(f.Departed)
	names := []sharedName{p.sharedName(), f.sharedName()}
	var copies []string
	for _, cp := range sharedConflictCopies(p.File) {
		cf, err := readSharedFile(cp)
		if err != nil || cf.ID != p.ID {
			continue // not a copy of this project, or not one tjek can read
		}
		incoming = append(incoming, cf.Tasks...)
		departed = joinDepartures(departed, cf.Departed)
		names = append(names, cf.sharedName())
		copies = append(copies, cp)
	}
	name := resolveSharedName(names)
	was := p.Name
	if name.Name != was {
		held := make(map[string]bool, len(incoming))
		for i := range incoming {
			held[incoming[i].ID] = true
		}
		if res.movedAside, err = moveOwnProjectAside(h, name.Name, held, b, by); err != nil {
			return res, err
		}
		if res.movedAside != "" {
			renameUndoProject(name.Name, res.movedAside)
			res.changed = true
		}
	}
	p.Name, p.Renamed, p.Former = name.Name, name.Renamed, name.Former
	// The tasks the file holds under a former name, and those names.
	strays := map[string]string{}
	for i := range incoming {
		if t := &incoming[i]; t.Project != p.Name && slices.Contains(p.Former, t.Project) {
			strays[t.ID] = t.Project
		}
	}
	if p.Name != was {
		p.Moving = append(p.Moving, was)
	}
	for _, f := range strays {
		p.Moving = append(p.Moving, f)
	}
	slices.Sort(p.Moving)
	p.Moving = slices.Compact(p.Moving)
	if !slices.Equal(p.Former, res.project.Former) || p.Name != was || !p.Renamed.Equal(res.project.Renamed) || len(p.Moving) > 0 {
		if err := adopt(p); err != nil {
			return res, err
		}
		res.project = p
	}
	if p.Name != was {
		if err := renameProjectTasks(h, was, p.Name, nil, b, by); err != nil {
			return res, err
		}
		renameUndoProject(was, p.Name)
		res.renamedFrom, res.changed = was, true
	}
	if len(incoming) > 0 {
		local, err := loadTodosForSync(h)
		if err != nil {
			return res, err
		}
		keepLocalStages(incoming, local)
		_, changed, err := mergeIntoStore(h, incoming, b)
		if err != nil {
			return res, err
		}
		res.changed = res.changed || changed
	}
	if len(strays) > 0 {
		n, err := autoEdit(h, b, by, func(t *todo.Todo, _ func(string) *todo.Todo) bool {
			if from, ok := strays[t.ID]; !ok || t.Project != from {
				return false
			}
			t.Project = p.Name
			return true
		})
		if err != nil {
			return res, err
		}
		res.changed = res.changed || n > 0
	}
	if len(p.Moving) > 0 {
		p.Moving = nil
		if err := adopt(p); err != nil {
			return res, err
		}
		res.project = p
	}
	if n, err := detachStraySubtasks(h, p.Name, b, by); err != nil {
		return res, err
	} else if n > 0 {
		res.changed = true
	}

	all, err := loadTodosForSync(h)
	if err != nil {
		return res, err
	}
	departed, gone := departures(p.Name, incoming, all, departed, time.Now())
	if len(gone) > 0 {
		if err := removeTasks(h, gone); err != nil {
			return res, err
		}
		forgetUndoOf(gone)
		res.changed, res.removed = true, gone
		if _, err := detachOrphans(h, gone, b, by); err != nil {
			return res, err
		}
		if all, err = loadTodosForSync(h); err != nil {
			return res, err
		}
	}
	f.Departed = departed
	f.Name, f.Renamed, f.Former = p.Name, p.Renamed, p.Former
	// A deleted task goes in only when the file already holds it, so the
	// others hear of the delete. One deleted before the project was shared
	// here, or before it ever reached the file, is nobody else's business.
	held := make(map[string]bool, len(incoming))
	for i := range incoming {
		held[incoming[i].ID] = true
	}
	f.Tasks = []todo.Todo{}
	for i := range all {
		if all[i].Project == p.Name && (!all[i].Deleted || held[all[i].ID]) {
			f.Tasks = append(f.Tasks, withoutStage(all[i]))
		}
	}
	f.Format = sharedFormat
	data, err := encodeSharedFile(f)
	if err != nil {
		return res, err
	}
	// Every write is an upload to everyone, and two devices that each
	// rewrote an unchanged file would make conflict copies of nothing.
	if old, err := os.ReadFile(p.File); err != nil || !bytes.Equal(old, data) {
		if err := writeFileAtomic(p.File, data, 0o644); err != nil {
			return res, err
		}
		res.wrote = true
	}
	for _, cp := range copies {
		_ = os.Remove(cp) // merged and written; one left behind is merged again
	}
	return res, nil
}

// A shared project's name ties its tasks to it on every device, so a rename
// travels through the file: the device that renames writes the new name with
// when it was set and the names before it, and every other device, on its
// next pass, moves its own tasks over (renameProjectTasks). Two renames made
// at once settle on the later one, the greater name breaking a tie, so every
// device ends on the same name whichever copy it read first.

// sharedName is a project's name as one holder has it.
type sharedName struct {
	Name    string
	Renamed time.Time
	Former  []string
}

func (p sharedProject) sharedName() sharedName { return sharedName{p.Name, p.Renamed, p.Former} }
func (f sharedFile) sharedName() sharedName    { return sharedName{f.Name, f.Renamed, f.Former} }

// resolveSharedName is the name the holders settle on, with every other name
// any of them had as its former names.
func resolveSharedName(names []sharedName) sharedName {
	win := names[0]
	former := map[string]bool{}
	for _, n := range names {
		former[n.Name] = true
		for _, f := range n.Former {
			former[f] = true
		}
		if n.Renamed.After(win.Renamed) || (n.Renamed.Equal(win.Renamed) && n.Name > win.Name) {
			win = n
		}
	}
	delete(former, win.Name)
	out := sharedName{Name: win.Name, Renamed: win.Renamed}
	for f := range former {
		out.Former = append(out.Former, f)
	}
	sort.Strings(out.Former)
	return out
}

// renameShared renames the shared project old to name for everyone sharing
// it: shared.json first (save, with old as Moving), then this device's tasks
// (finishMoving), then the file. A
// file that cannot be reached now takes the name on a later pass. A project
// of the new name already here would be handed to everyone, so it is refused.
func renameShared(h *sql.DB, c *sharedConfig, old, name string, b rank.Biases, by editor, save func(sharedConfig) error) (sharedProject, error) {
	name = strings.TrimSpace(name)
	p, ok := c.find(old)
	switch {
	case !ok:
		return p, fmt.Errorf("%q is not a shared project", old)
	case name == "" || name == old:
		return p, errors.New("no new name")
	}
	if _, taken := c.find(name); taken {
		return p, fmt.Errorf("%q is already shared", name)
	}
	todos, err := loadTodosForSync(h)
	if err != nil {
		return p, err
	}
	if localProjectTasks(todos, name) > 0 {
		return p, fmt.Errorf("there is already a project named %q here", name)
	}
	p.Former = slices.DeleteFunc(append(slices.Clone(p.Former), old), func(f string) bool { return f == name })
	slices.Sort(p.Former)
	p.Former = slices.Compact(p.Former)
	p.Name, p.Renamed, p.Moving = name, time.Now(), []string{old}
	c.set(p)
	if err := save(*c); err != nil {
		return p, err
	}
	p, err = finishMoving(h, p, b, by, func(q sharedProject) error {
		c.set(q)
		return save(*c)
	})
	if err != nil {
		return p, err
	}
	if _, err := syncShared(h, p, b, by, func(q sharedProject) error {
		c.set(q)
		return save(*c)
	}); err != nil {
		return p, fmt.Errorf("renamed here; the others get the name once %s can be reached: %w", filepath.Base(p.File), err)
	}
	return p, nil
}

// moveOwnProjectAside clears the way for a shared project renamed elsewhere
// to name: this device's own tasks in a project of that name, the ones the
// file does not hold, move to the first free "name (2)", "name (3)", …, which
// it returns ("" when there were none). Taking the name as it is would put
// them in the shared project, and hand them to everyone.
func moveOwnProjectAside(h *sql.DB, name string, held map[string]bool, b rank.Biases, by editor) (string, error) {
	all, err := loadTodosForSync(h)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	own := false
	for i := range all {
		taken[all[i].Project] = true
		own = own || (all[i].Project == name && !held[all[i].ID])
	}
	if !own {
		return "", nil
	}
	aside := name
	for n := 2; taken[aside]; n++ {
		aside = fmt.Sprintf("%s (%d)", name, n)
	}
	return aside, renameProjectTasks(h, name, aside, held, b, by)
}

// renameProjectTasks moves every task of the project from to the project to,
// but the ones skip names.
func renameProjectTasks(h *sql.DB, from, to string, skip map[string]bool, b rank.Biases, by editor) error {
	_, err := autoEdit(h, b, by, func(t *todo.Todo, _ func(string) *todo.Todo) bool {
		if t.Project != from || skip[t.ID] {
			return false
		}
		t.Project = to
		return true
	})
	return err
}

// finishMoving moves the project's tasks off the names p.Moving holds, onto
// p.Name, and clears Moving (adopt): the end of a move a pass or a rename was
// cut short in.
func finishMoving(h *sql.DB, p sharedProject, b rank.Biases, by editor, adopt func(sharedProject) error) (sharedProject, error) {
	for _, from := range p.Moving {
		if from == p.Name {
			continue
		}
		if err := renameProjectTasks(h, from, p.Name, nil, b, by); err != nil {
			return p, err
		}
		renameUndoProject(from, p.Name)
	}
	p.Moving = nil
	return p, adopt(p)
}

// detachStraySubtasks makes each live subtask in the project whose parent is
// not in it a task of its own, and returns how many it made. The others
// sharing the project do not have that parent, so to them it would be a
// subtask of nothing, and no list would show it.
func detachStraySubtasks(h *sql.DB, project string, b rank.Biases, by editor) (int, error) {
	return autoEdit(h, b, by, func(t *todo.Todo, byID func(string) *todo.Todo) bool {
		if t.Project != project || t.Deleted || t.ParentID == "" {
			return false
		}
		if parent := byID(t.ParentID); parent != nil && parent.Project == project {
			return false
		}
		t.ParentID = ""
		return true
	})
}

// detachOrphans makes each live task whose parent is among the tasks removed
// outright a task of its own, and returns how many it made. A subtask filed
// in another project stays when its shared parent goes, and under a parent
// the store no longer holds, no list would show it.
func detachOrphans(h *sql.DB, removed []string, b rank.Biases, by editor) (int, error) {
	if len(removed) == 0 {
		return 0, nil
	}
	gone := make(map[string]bool, len(removed))
	for _, id := range removed {
		gone[id] = true
	}
	return autoEdit(h, b, by, func(t *todo.Todo, _ func(string) *todo.Todo) bool {
		if t.Deleted || !gone[t.ParentID] {
			return false
		}
		t.ParentID = ""
		return true
	})
}

// autoEdit saves the tasks edit changes, as a change of tjek's own signed by
// by (todo.Todo.Auto), and returns how many it saved. edit is handed each
// stored task, tombstones included, with a lookup of the others by ID, and
// reports whether it changed it.
func autoEdit(h *sql.DB, b rank.Biases, by editor, edit func(t *todo.Todo, byID func(string) *todo.Todo) bool) (int, error) {
	all, err := loadTodosForSync(h)
	if err != nil {
		return 0, err
	}
	index := make(map[string]int, len(all))
	for i := range all {
		index[all[i].ID] = i
	}
	byID := func(id string) *todo.Todo {
		if i, ok := index[id]; ok {
			return &all[i]
		}
		return nil
	}
	var changed, live []*todo.Todo
	for i := range all {
		t := &all[i]
		if edit(t, byID) {
			t.Auto = true
			changed = append(changed, t)
		}
		if !t.Deleted {
			live = append(live, t)
		}
	}
	if len(changed) == 0 {
		return 0, nil
	}
	now := time.Now()
	rk := rank.Ranker{Biases: b}.Refreshed(now, live)
	if err := saveStamped(h, changed, nil, rk.ScoreNow(), now, by); err != nil {
		return 0, err
	}
	noteForeignWrite()
	return len(changed), nil
}

// A task moved out of a shared project leaves the file, and the others must
// hear of it: a device that still held it in the project would write it back
// on every pass, the device that moved it would take it out again, and the
// two would rewrite the file against each other for good. So the file
// remembers each move out (Departed: the task's ID and the stamp of its
// project), and a device holding the task in the project from before the
// move removes it outright, as leaving does: the task is still someone's, so
// it must not be deleted for them. The record holds nothing of the task, so
// a task moved out to be kept private stays private, and it stays as long as
// tombstones do, so a device that was away when the task left still hears.
// A task moved back in after its move out is the project's again.

// departures is the file's record of the tasks moved out of the project,
// with the moves this device made, less the ones older than a tombstone is
// kept (tombstoneRetention), and the tasks this device must remove because
// they left. incoming is what the file and its copies hold, local the store
// after the merge.
func departures(project string, incoming, local []todo.Todo, known map[string]hlc.Stamp, now time.Time) (map[string]hlc.Stamp, []string) {
	out := maps.Clone(known)
	if out == nil {
		out = make(map[string]hlc.Stamp)
	}
	cutoff := now.Add(-tombstoneRetention)
	maps.DeleteFunc(out, func(_ string, s hlc.Stamp) bool { return s.Time().Before(cutoff) })
	byID := make(map[string]*todo.Todo, len(local))
	for i := range local {
		byID[local[i].ID] = &local[i]
	}
	// The merge kept the later project, so a task the file holds in the
	// project and the store holds out of it was moved out here.
	for i := range incoming {
		if incoming[i].Project != project {
			continue
		}
		if t := byID[incoming[i].ID]; t != nil && t.Project != project {
			out = joinDepartures(out, map[string]hlc.Stamp{t.ID: t.Stamp(projectKey)})
		}
	}
	var gone []string
	for id, s := range out {
		t := byID[id]
		if t == nil || t.Project != project {
			continue
		}
		if t.Stamp(projectKey).After(s) {
			delete(out, id) // moved back in since
			continue
		}
		gone = append(gone, id)
	}
	sort.Strings(gone)
	if len(out) == 0 {
		out = nil
	}
	return out, gone
}

// joinDepartures is the union of two records of moves out, keeping the later
// move of a task in both.
func joinDepartures(a, b map[string]hlc.Stamp) map[string]hlc.Stamp {
	if len(b) == 0 {
		return a
	}
	if a == nil {
		a = make(map[string]hlc.Stamp, len(b))
	}
	for id, s := range b {
		a[id] = hlc.Max(a[id], s)
	}
	return a
}

// removeTasks deletes the tasks ids outright, as removeProjectTasks does.
func removeTasks(h *sql.DB, ids []string) error {
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := removeTaskRows(tx, ids); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	noteForeignWrite()
	return nil
}

// projectKey is the merge unit of a task's project (todo.Fields).
const projectKey = "project"

// stageKey is the merge unit of a task's board column (todo.Fields).
const stageKey = "stage"

// A shared project's board columns are each person's own: people sharing a
// project need not have the same columns, and a card one of them moved would
// land in the other's first column. So the file carries no column
// (withoutStage), and what comes in keeps the one this device gave it
// (keepLocalStages). Done is a status, not a column, and is shared.

// withoutStage is t as the file holds it: no column, no stamp for one, and
// no history of moving between columns.
func withoutStage(t todo.Todo) todo.Todo {
	t.Stage = ""
	if _, ok := t.Stamps[stageKey]; ok {
		t.Stamps = maps.Clone(t.Stamps)
		delete(t.Stamps, stageKey)
	}
	if slices.ContainsFunc(t.History, func(e todo.Event) bool { return slices.Contains(e.Fields, stageKey) }) {
		hist := make([]todo.Event, 0, len(t.History))
		for _, e := range t.History {
			if slices.Contains(e.Fields, stageKey) {
				e.Fields = slices.DeleteFunc(slices.Clone(e.Fields), func(f string) bool { return f == stageKey })
				if e.Action == todo.ActionEdited && len(e.Fields) == 0 {
					continue
				}
			}
			hist = append(hist, e)
		}
		t.History = hist
	}
	return t
}

// keepLocalStages gives each incoming task the column local holds for it, with
// its stamp, so the merge leaves the column as it is here; a task new to this
// device starts in the first column. It also covers a file written by a tjek
// that still shared columns.
func keepLocalStages(incoming, local []todo.Todo) {
	own := make(map[string]*todo.Todo, len(local))
	for i := range local {
		own[local[i].ID] = &local[i]
	}
	for i := range incoming {
		t := &incoming[i]
		*t = withoutStage(*t)
		l := own[t.ID]
		if l == nil {
			continue
		}
		t.Stage = l.Stage
		if s, ok := l.Stamps[stageKey]; ok {
			t.Stamps = maps.Clone(t.Stamps)
			if t.Stamps == nil {
				t.Stamps = make(map[string]hlc.Stamp)
			}
			t.Stamps[stageKey] = s
		}
	}
}

// encodeSharedFile writes the file in one canonical form: tasks in ID order,
// each in the sync digest's form (tasksync.CanonicalJSON: its sets and
// records sorted, its times in UTC). Two devices holding the same project
// must write the same bytes, or each would see the other's file as
// different and rewrite it on every pass, for good; the store's own order of
// a task's comments or tags, and the device's time zone, are not the
// project's content. The order also keeps an unchanged task in its place,
// so a synced folder uploads a small diff.
func encodeSharedFile(f sharedFile) ([]byte, error) {
	sort.Slice(f.Tasks, func(i, j int) bool { return f.Tasks[i].ID < f.Tasks[j].ID })
	tasks := make([]json.RawMessage, len(f.Tasks))
	for i := range f.Tasks {
		tasks[i] = tasksync.CanonicalJSON(f.Tasks[i])
	}
	data, err := json.MarshalIndent(struct {
		Format   int                  `json:"format"`
		ID       string               `json:"id"`
		Name     string               `json:"name"`
		Created  time.Time            `json:"created"`
		Tasks    []json.RawMessage    `json:"tasks"`
		Departed map[string]hlc.Stamp `json:"departed,omitempty"`
		Renamed  time.Time            `json:"renamed,omitzero"`
		Former   []string             `json:"former,omitempty"`
	}{f.Format, f.ID, f.Name, f.Created.UTC(), tasks, f.Departed, f.Renamed.UTC(), f.Former}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// sharedPass is what syncAllShared did: whether it changed the store, the
// renames it took up (old name to new), the projects of this device's own
// those renames moved aside (name to where it went), and the tasks it
// removed outright.
type sharedPass struct {
	changed bool
	renames map[string]string
	aside   map[string]string
	removed []string
}

// syncAllShared runs syncShared for every project this device shares. A
// project that fails does not stop the others; the errors come back together.
// A rename one of them takes up is saved to shared.json.
func syncAllShared(h *sql.DB, b rank.Biases, by editor) (pass sharedPass, err error) {
	c, err := loadSharedConfig()
	if err != nil || len(c.Projects) == 0 {
		return pass, err
	}
	var errs []error
	for _, p := range slices.Clone(c.Projects) {
		res, err := syncShared(h, p, b, by, func(q sharedProject) error {
			c.set(q)
			return saveSharedConfig(c)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
			continue
		}
		pass.changed = pass.changed || res.changed
		pass.removed = append(pass.removed, res.removed...)
		if res.renamedFrom != "" {
			if pass.renames == nil {
				pass.renames, pass.aside = map[string]string{}, map[string]string{}
			}
			pass.renames[res.renamedFrom] = res.project.Name
			if res.movedAside != "" {
				pass.aside[res.project.Name] = res.movedAside
			}
		}
	}
	return pass, errors.Join(errs...)
}
