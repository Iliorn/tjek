package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Iliorn/tjek/hlc"
	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/tasksync"
	"github.com/Iliorn/tjek/todo"
)

// sharer is one person's device in a shared-project test: a store of their
// own and their shared.json, both in memory.
type sharer struct {
	h   *sql.DB
	cfg sharedConfig
	by  editor
}

func newSharer(t *testing.T, name string) *sharer {
	return &sharer{h: openTestDB(t), by: editor{name: name}}
}

func (s *sharer) save(t *testing.T, at time.Time, tasks ...todo.Todo) {
	t.Helper()
	historySave(t, s.h, at, s.by, tasks)
}

func (s *sharer) share(t *testing.T, name, folder string) sharedProject {
	t.Helper()
	p, err := startSharing(&s.cfg, name, folder)
	if err != nil {
		t.Fatalf("%s sharing %q: %v", s.by.name, name, err)
	}
	return p
}

func (s *sharer) join(t *testing.T, path string) sharedProject {
	t.Helper()
	p, err := joinShared(&s.cfg, path)
	if err != nil {
		t.Fatalf("%s joining %s: %v", s.by.name, path, err)
	}
	return p
}

func (s *sharer) sync(t *testing.T, name string) sharedResult {
	t.Helper()
	p, ok := s.cfg.find(name)
	if !ok {
		t.Fatalf("%s does not share %q", s.by.name, name)
	}
	res, err := syncShared(s.h, p, rank.Biases{}, s.by, func(q sharedProject) error {
		s.cfg.set(q)
		return nil
	})
	if err != nil {
		t.Fatalf("%s syncing %q: %v", s.by.name, name, err)
	}
	return res
}

func (s *sharer) task(t *testing.T, id string) (todo.Todo, bool) {
	t.Helper()
	all, err := loadTodosForSync(s.h)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range all {
		if x.ID == id {
			return x, true
		}
	}
	return todo.Todo{}, false
}

// tripTask is a task in the project the tests share.
func tripTask(title string) todo.Todo {
	t := todo.New(title)
	t.Project = "Trip"
	return t
}

// Anna shares a project in a file and Mark joins it: he gets its tasks and
// none of her others, and each one's changes reach the other with the
// history saying who made them.
func TestTwoPeopleShareAProjectInOneFile(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	trip, private := tripTask("Book the ferry"), todo.New("Dentist")
	anna.save(t, s0, trip, private)

	p := anna.share(t, "Trip", folder)
	if p.File != filepath.Join(folder, "Trip.tjek") {
		t.Fatalf("shared in %s, want Trip.tjek in the folder", p.File)
	}
	anna.sync(t, "Trip")
	entries, _ := os.ReadDir(folder)
	if len(entries) != 1 {
		t.Fatalf("the folder holds %d entries, want the one file", len(entries))
	}

	mark.join(t, p.File)
	mark.sync(t, "Trip")
	got, ok := mark.task(t, trip.ID)
	if !ok || got.Title != "Book the ferry" {
		t.Fatalf("Mark did not receive the shared task: %+v", got)
	}
	if _, ok := mark.task(t, private.ID); ok {
		t.Fatal("a task outside the shared project reached Mark")
	}

	got.Toggle()
	mark.save(t, s0.Add(time.Minute), got)
	mark.sync(t, "Trip")
	anna.sync(t, "Trip")
	mine, _ := anna.task(t, trip.ID)
	if mine.Status != todo.Done {
		t.Fatal("Mark's close did not reach Anna")
	}
	if last := mine.History[len(mine.History)-1]; last.Action != todo.ActionClosed || last.Author != "Mark" {
		t.Errorf("Anna's history ends %+v, want closed by Mark", last)
	}
}

// Two people editing different fields of one task at once both keep their
// edit, whichever of them syncs first.
func TestSharedEditsToDifferentFieldsBothSurvive(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	task := tripTask("Plan the route")
	anna.save(t, s0, task)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")

	a, _ := anna.task(t, task.ID)
	a.SetDueDate(s0.Add(72 * time.Hour))
	anna.save(t, s0.Add(time.Minute), a)
	m, _ := mark.task(t, task.ID)
	m.SetPriority(todo.PriorityHigh)
	mark.save(t, s0.Add(2*time.Minute), m)

	anna.sync(t, "Trip")
	mark.sync(t, "Trip")
	anna.sync(t, "Trip")
	for _, s := range []*sharer{anna, mark} {
		got, _ := s.task(t, task.ID)
		if got.DueDate.IsZero() || got.Priority != todo.PriorityHigh {
			t.Errorf("%s has due %v, priority %v; want both edits", s.by.name, got.DueDate, got.Priority)
		}
	}
}

// Each person's board columns are their own: the file carries no column, a
// card stays where each of them put it whoever moves it next, and closing it
// still reaches everyone.
func TestSharedProjectColumnsArePersonal(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	task := tripTask("Pack the tent")
	task.SetStage("Doing")
	anna.save(t, s0, task)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")

	mark.join(t, p.File)
	mark.sync(t, "Trip")
	if got, _ := mark.task(t, task.ID); got.Stage != "" {
		t.Errorf("Mark got the task in %q, want his first column", got.Stage)
	}

	m, _ := mark.task(t, task.ID)
	m.SetStage("Review")
	mark.save(t, s0.Add(time.Minute), m)
	mark.sync(t, "Trip")
	anna.sync(t, "Trip")
	mark.sync(t, "Trip")
	for s, want := range map[*sharer]string{anna: "Doing", mark: "Review"} {
		if got, _ := s.task(t, task.ID); got.Stage != want {
			t.Errorf("%s has the task in %q, want %q", s.by.name, got.Stage, want)
		}
	}
	data, _ := os.ReadFile(p.File)
	if strings.Contains(string(data), `"stage"`) {
		t.Errorf("the file carries a column:\n%s", data)
	}
	a, _ := anna.task(t, task.ID)
	for _, e := range a.History {
		if e.Author == "Mark" && e.Action == todo.ActionEdited {
			t.Errorf("Anna's history shows Mark's move between his columns: %+v", e)
		}
	}
	if res := anna.sync(t, "Trip"); res.wrote || res.changed {
		t.Errorf("devices that agree kept syncing: %+v", res)
	}

	m, _ = mark.task(t, task.ID)
	m.Toggle()
	mark.save(t, s0.Add(2*time.Minute), m)
	mark.sync(t, "Trip")
	anna.sync(t, "Trip")
	if a, _ := anna.task(t, task.ID); a.Status != todo.Done {
		t.Error("Mark's close did not reach Anna")
	}
}

// Two devices that wrote the file at once leave a conflict copy beside it,
// the way cloud services do; the next pass merges the copy in, writes the
// result, and removes the copy.
func TestConflictCopiesAreMergedAndRemoved(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	task := tripTask("Pack")
	anna.save(t, s0, task)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")

	// Both edit; Mark's write lands as the conflict copy, and the file keeps
	// the version they both started from.
	base, err := os.ReadFile(p.File)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := anna.task(t, task.ID)
	a.AddComment("passports")
	anna.save(t, s0.Add(time.Minute), a)
	m, _ := mark.task(t, task.ID)
	m.SetPriority(todo.PriorityHigh)
	mark.save(t, s0.Add(time.Minute), m)
	mark.sync(t, "Trip")
	copyPath := filepath.Join(folder, "Trip (Mark's conflicted copy 2026-09-29).tjek")
	if err := os.Rename(p.File, copyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.File, base, 0o644); err != nil {
		t.Fatal(err)
	}
	anna.sync(t, "Trip")

	got, _ := anna.task(t, task.ID)
	if got.Priority != todo.PriorityHigh || len(got.Comments) != 1 {
		t.Fatalf("Anna has priority %v and %d comment(s), want both edits", got.Priority, len(got.Comments))
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Error("the conflict copy is still there")
	}
	f, err := readSharedFile(p.File)
	if err != nil || len(f.Tasks) != 1 || f.Tasks[0].Priority != todo.PriorityHigh || len(f.Tasks[0].Comments) != 1 {
		t.Errorf("the file does not hold both edits after the merge (%v)", err)
	}
}

// A cloud service that lets a later write replace an earlier one loses that
// write only for a moment: the device that made it still holds it, sees the
// file lacks it, and writes it back.
func TestAnOverwrittenChangeComesBack(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	task := tripTask("Pack")
	anna.save(t, s0, task)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")
	stale, err := os.ReadFile(p.File)
	if err != nil {
		t.Fatal(err)
	}

	a, _ := anna.task(t, task.ID)
	a.SetNotes("the big suitcase")
	anna.save(t, s0.Add(time.Minute), a)
	anna.sync(t, "Trip")
	// Mark's client, not having seen it, writes his older copy over it.
	if err := os.WriteFile(p.File, stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if res := anna.sync(t, "Trip"); !res.wrote {
		t.Fatal("Anna did not put her change back into the file")
	}
	mark.sync(t, "Trip")
	if got, _ := mark.task(t, task.ID); got.Notes != "the big suitcase" {
		t.Errorf("Mark has notes %q, want Anna's change", got.Notes)
	}
}

// Once two devices agree, neither rewrites the file: each would otherwise
// see the other's write as a change and answer it, forever.
func TestAgreeingDevicesStopWriting(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	task := tripTask("Pack")
	task.AddTag("bags")
	task.AddTag("abroad")
	task.AddComment("first")
	task.AddComment("second")
	anna.save(t, s0, task)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	for i := 0; i < 2; i++ {
		mark.sync(t, "Trip")
		anna.sync(t, "Trip")
	}
	if mark.sync(t, "Trip").wrote || anna.sync(t, "Trip").wrote {
		t.Error("a device rewrote a file that already said what it holds")
	}
}

// A task Anna moves out of the shared project leaves Mark's device too, with
// nothing of it left in the file, and the two stop rewriting the file; moved
// back in, it returns to him.
func TestATaskMovedOutOfASharedProjectLeavesEveryone(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	ferry, gift := tripTask("Book the ferry"), tripTask("Mark's birthday present")
	anna.save(t, s0, ferry, gift)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")

	moved, _ := anna.task(t, gift.ID)
	moved.Project = "Private"
	anna.save(t, s0.Add(time.Minute), moved)
	anna.sync(t, "Trip")
	if res := mark.sync(t, "Trip"); !res.changed {
		t.Error("Mark's pass did not report the removal")
	}
	if _, ok := mark.task(t, gift.ID); ok {
		t.Fatal("the task moved out is still on Mark's device")
	}
	if got, _ := anna.task(t, gift.ID); got.Project != "Private" || got.Deleted {
		t.Fatalf("Anna's task is %+v, want it kept in Private", got)
	}
	if _, ok := mark.task(t, ferry.ID); !ok {
		t.Fatal("the task still in the project left Mark's device too")
	}
	data, _ := os.ReadFile(p.File)
	if strings.Contains(string(data), "birthday") || strings.Contains(string(data), "Private") {
		t.Errorf("the file still tells of the task moved out:\n%s", data)
	}
	anna.sync(t, "Trip")
	if mark.sync(t, "Trip").wrote || anna.sync(t, "Trip").wrote {
		t.Error("the devices still rewrite the file after the move out")
	}

	// A tjek without the record writes the task back as it last knew it.
	stale := moved
	stale.Project = "Trip"
	stale.Stamps = nil
	stale.ModifiedAt = s0
	f, _ := readSharedFile(p.File)
	f.Tasks, f.Departed = append(f.Tasks, stale), nil
	data, _ = encodeSharedFile(f)
	if err := os.WriteFile(p.File, data, 0o644); err != nil {
		t.Fatal(err)
	}
	mark.sync(t, "Trip")
	anna.sync(t, "Trip")
	mark.sync(t, "Trip")
	if _, ok := mark.task(t, gift.ID); ok {
		t.Error("an older tjek's copy brought the task back to Mark for good")
	}

	back, _ := anna.task(t, gift.ID)
	back.Project = "Trip"
	anna.save(t, s0.Add(2*time.Minute), back)
	anna.sync(t, "Trip")
	mark.sync(t, "Trip")
	if got, ok := mark.task(t, gift.ID); !ok || got.Project != "Trip" {
		t.Errorf("moved back in, the task did not return to Mark: %+v", got)
	}
	if mark.sync(t, "Trip").wrote || anna.sync(t, "Trip").wrote {
		t.Error("the devices still rewrite the file after the move back")
	}
}

// The record of a move out is kept as long as a tombstone, then dropped, so
// the file does not grow with every task that ever left the project.
func TestOldDeparturesAreDropped(t *testing.T) {
	now := s0.Add(tombstoneRetention + 24*time.Hour)
	known := map[string]hlc.Stamp{"old": hlc.At(s0), "recent": hlc.At(now.Add(-time.Hour))}
	got, gone := departures("Trip", nil, nil, known, now)
	if len(got) != 1 || got["recent"] == "" || len(gone) != 0 {
		t.Errorf("departures kept %v (gone %v), want only the recent move", got, gone)
	}
	if len(known) != 2 {
		t.Error("departures changed the record it was handed")
	}
}

// Anna's timer on a shared task is hers: on Mark's device it does not count
// as his running timer, starting or stopping his own leaves it running, and
// his recovery of abandoned timers neither stops it nor keeps it fresh. On a
// task of his own nothing changes.
func TestASharedTaskTimerIsItsStartersOwn(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	ferry := tripTask("Book the ferry")
	anna.save(t, s0, ferry)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	errand := todo.New("Post office")
	errand.StartTimer()
	errand.TimeEntries[0].StartedAt = time.Now().Add(-6 * time.Hour)
	errand.TimeEntries[0].LastSeen = errand.TimeEntries[0].StartedAt
	mark.save(t, s0, errand)

	hers, _ := anna.task(t, ferry.ID)
	hers.StartTimer()
	hers.TimeEntries[0].StartedAt = time.Now().Add(-6 * time.Hour)
	hers.TimeEntries[0].LastSeen = hers.TimeEntries[0].StartedAt
	anna.save(t, s0.Add(time.Minute), hers)
	anna.sync(t, "Trip")
	mark.sync(t, "Trip")
	sc := mark.cfg.timerScope("Mark")

	all, _ := loadTodosForSync(mark.h)
	s := &Store{}
	s.setTimerScope(sc)
	for _, x := range all {
		s.add(x)
	}
	if _, ok := s.runningTimers[ferry.ID]; ok {
		t.Error("Anna's timer counts as Mark's running timer")
	}
	if _, ok := s.runningTimers[errand.ID]; !ok {
		t.Error("Mark's own timer is not his running timer")
	}
	s.startTimer(ferry.ID)
	if got := s.get(ferry.ID); got.RunningEntryBy("Anna") == nil || !got.TimerRunningBy("Mark") {
		t.Fatalf("t on the shared task: entries %+v, want Anna's still running beside Mark's", got.TimeEntries)
	}
	s.stopTimer(ferry.ID)
	if got := s.get(ferry.ID); got.RunningEntryBy("Anna") == nil || got.TimerRunningBy("Mark") {
		t.Fatalf("stopping Mark's timer: entries %+v, want only his stopped", got.TimeEntries)
	}

	marks := make([]todo.Todo, len(all))
	copy(marks, all)
	for i := range marks {
		marks[i].TimeEntries = slices.Clone(marks[i].TimeEntries)
	}
	if stopped := stopOtherRunningTimers(marks, "", sc); len(stopped) != 1 || stopped[0].ID != errand.ID {
		t.Errorf("starting a timer stops %v, want only Mark's errand", stopped)
	}

	rec, err := reconcileStaleTimers(mark.h, time.Now(), idleThreshold, sc)
	if err != nil || len(rec) != 1 || rec[0].Title != "Post office" {
		t.Errorf("Mark's recovery stopped %+v (%v), want only his own errand", rec, err)
	}
	if err := heartbeatRunningTimers(mark.h, time.Now(), sc); err != nil {
		t.Fatal(err)
	}
	if got, _ := mark.task(t, ferry.ID); time.Since(got.TimeEntries[0].LastSeen) < time.Hour {
		t.Error("Mark's heartbeat kept Anna's timer fresh")
	}
	mark.sync(t, "Trip")
	anna.sync(t, "Trip")
	if got, _ := anna.task(t, ferry.ID); !got.IsTimerRunning() {
		t.Error("Anna's timer was stopped by Mark's device")
	}
	if rec, _ := reconcileStaleTimers(anna.h, time.Now(), idleThreshold, anna.cfg.timerScope("Anna")); len(rec) != 1 {
		t.Errorf("Anna's own recovery stopped %d timer(s), want her abandoned one", len(rec))
	}
}

// Anna renames the shared project; on Mark's next pass his shared.json and
// every task of his take the new name, a task he made since his last pass
// included, and nothing leaves the project. Two renames at once settle on
// the later one on both devices.
func TestASharedProjectRenameReachesEveryone(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	ferry := tripTask("Book the ferry")
	anna.save(t, s0, ferry)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")
	bags := tripTask("Pack the bags")
	mark.save(t, s0.Add(time.Minute), bags)

	save := func(sharedConfig) error { return nil }
	if _, err := renameShared(anna.h, &anna.cfg, "Trip", "Summer", rank.Biases{}, anna.by, save); err != nil {
		t.Fatal(err)
	}
	res := mark.sync(t, "Trip")
	if res.renamedFrom != "Trip" || res.project.Name != "Summer" {
		t.Errorf("Mark's pass: %+v, want the rename taken up", res)
	}
	if _, ok := mark.cfg.find("Summer"); !ok {
		t.Fatalf("Mark's shared.json: %+v", mark.cfg.Projects)
	}
	for _, id := range []string{ferry.ID, bags.ID} {
		if got, _ := mark.task(t, id); got.Project != "Summer" {
			t.Errorf("Mark's %q is in %q, want Summer", got.Title, got.Project)
		}
	}
	anna.sync(t, "Summer")
	if got, ok := anna.task(t, bags.ID); !ok || got.Project != "Summer" {
		t.Errorf("the task Mark made before the rename reached Anna as %+v", got)
	}
	if mark.sync(t, "Summer").wrote || anna.sync(t, "Summer").wrote {
		t.Error("the devices still rewrite the file after the rename")
	}

	// Both rename before either syncs; Mark's is the later.
	if _, err := renameShared(anna.h, &anna.cfg, "Summer", "Holiday", rank.Biases{}, anna.by, save); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	p, _ = mark.cfg.find("Summer")
	p.Former, p.Name, p.Renamed = []string{"Summer", "Trip"}, "Vacation", time.Now()
	mark.cfg.set(p)
	if err := renameProjectTasks(mark.h, "Summer", "Vacation", nil, rank.Biases{}, mark.by); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		mark.sync(t, mark.cfg.Projects[0].Name)
		anna.sync(t, anna.cfg.Projects[0].Name)
	}
	for _, s := range []*sharer{anna, mark} {
		if got := s.cfg.Projects[0].Name; got != "Vacation" {
			t.Errorf("%s's project is called %q, want the later rename", s.by.name, got)
		}
		for _, id := range []string{ferry.ID, bags.ID} {
			if got, _ := s.task(t, id); got.Project != "Vacation" {
				t.Errorf("%s's %q is in %q", s.by.name, got.Title, got.Project)
			}
		}
	}
}

// Leaving removes the project's tasks from this device outright and leaves
// the file to the others; joining again brings every task back, history and
// all, and deletes nothing for anyone.
func TestLeavingRemovesTheTasksAndRejoiningBringsThemBack(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	task, private := tripTask("Pack"), todo.New("Dentist")
	anna.save(t, s0, task)
	mark.save(t, s0, private)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")

	_, ids, err := leaveShared(mark.h, &mark.cfg, "Trip", rank.Biases{}, mark.by)
	if err != nil || len(ids) != 1 {
		t.Fatalf("leave: removed %d, %v; want the one task", len(ids), err)
	}
	if len(mark.cfg.Projects) != 0 || len(mark.cfg.Left) != 1 {
		t.Fatalf("after leaving: shared %+v, left %+v", mark.cfg.Projects, mark.cfg.Left)
	}
	if _, ok := mark.task(t, task.ID); ok {
		t.Error("the project's task is still on Mark's device, tombstone or not")
	}
	if _, ok := mark.task(t, private.ID); !ok {
		t.Error("leaving removed a task outside the project")
	}
	if _, err := os.Stat(p.File); err != nil {
		t.Errorf("leaving took the file away from the others: %v", err)
	}
	if _, _, err := leaveShared(mark.h, &mark.cfg, "Trip", rank.Biases{}, mark.by); err == nil {
		t.Error("leaving a project that is not shared should say so")
	}

	a, _ := anna.task(t, task.ID)
	a.AddComment("don't forget the charger")
	anna.save(t, s0.Add(time.Minute), a)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	if len(mark.cfg.Left) != 0 {
		t.Error("rejoining did not forget the removed tasks")
	}
	mark.sync(t, "Trip")
	anna.sync(t, "Trip")
	got, ok := mark.task(t, task.ID)
	if !ok || len(got.Comments) != 1 || len(got.History) == 0 {
		t.Fatalf("rejoined copy %+v, want the task with Anna's comment and its history", got)
	}
	if still, _ := anna.task(t, task.ID); still.Deleted {
		t.Fatal("Mark's leave and rejoin deleted the task for Anna")
	}
}

// Once a rename has moved the tasks, the old name is free: a project of
// one's own by that name syncs like any other. While the move runs it is
// kept out (Moving), and a move cut short is finished by the next pass.
func TestARenamedProjectsOldNameIsFreeAgain(t *testing.T) {
	setTestHome(t, t.TempDir())
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	ferry := tripTask("Book the ferry")
	anna.save(t, s0, ferry)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")

	save := func(sharedConfig) error { return nil }
	if _, err := renameShared(anna.h, &anna.cfg, "Trip", "Summer", rank.Biases{}, anna.by, save); err != nil {
		t.Fatal(err)
	}
	mine := tripTask("A new Trip of Anna's own")
	for _, c := range []sharedConfig{anna.cfg, mark.cfg} {
		if c.Projects[0].Moving != nil {
			t.Errorf("a finished rename left Moving %v", c.Projects[0].Moving)
		}
	}
	if anna.cfg.keepsOutOfSync(&mine) {
		t.Error("the old name is still kept from the sync server after the rename")
	}
	inTransit := anna.cfg
	inTransit.Projects = []sharedProject{anna.cfg.Projects[0]}
	inTransit.Projects[0].Moving = []string{"Trip"}
	if !inTransit.keepsOutOfSync(&mine) {
		t.Error("a name the tasks are being moved off is not kept from the sync server")
	}

	// Mark's pass took the rename up; cut it short after shared.json.
	mark.sync(t, "Trip")
	cut := mark.cfg.Projects[0]
	cut.Moving = []string{"Trip"}
	mark.cfg.set(cut)
	if err := renameProjectTasks(mark.h, "Summer", "Trip", nil, rank.Biases{}, mark.by); err != nil {
		t.Fatal(err)
	}
	mark.sync(t, "Summer")
	if got, _ := mark.task(t, ferry.ID); got.Project != "Summer" || mark.cfg.Projects[0].Moving != nil {
		t.Errorf("after the pass: task in %q, Moving %v; want the move finished", got.Project, mark.cfg.Projects[0].Moving)
	}
}

// A task a device files under the old name before it hears of a rename
// reaches the others under the new one; a project of their own that took
// the old name in the meantime keeps its tasks.
func TestATaskFiledUnderAFormerNameMovesToTheNewOne(t *testing.T) {
	setTestHome(t, t.TempDir())
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	anna.save(t, s0, tripTask("Book the ferry"))
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")
	before, _ := os.ReadFile(p.File)

	save := func(sharedConfig) error { return nil }
	if _, err := renameShared(anna.h, &anna.cfg, "Trip", "Summer", rank.Biases{}, anna.by, save); err != nil {
		t.Fatal(err)
	}
	private := tripTask("Anna's own new Trip")
	anna.save(t, s0.Add(time.Hour), private)

	// Mark, not yet aware, adds a task and writes the file at the same time
	// as Anna: his write lands as a conflict copy of the file he had.
	late := tripTask("Pack the bags")
	mark.save(t, s0.Add(time.Hour), late)
	if err := os.WriteFile(strings.TrimSuffix(p.File, ".tjek")+" (1).tjek", before, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := readSharedFile(strings.TrimSuffix(p.File, ".tjek") + " (1).tjek")
	got, _ := mark.task(t, late.ID)
	f.Tasks = append(f.Tasks, got)
	data, _ := encodeSharedFile(f)
	if err := os.WriteFile(strings.TrimSuffix(p.File, ".tjek")+" (1).tjek", data, 0o644); err != nil {
		t.Fatal(err)
	}

	anna.sync(t, "Summer")
	if got, _ := anna.task(t, late.ID); got.Project != "Summer" {
		t.Errorf("Mark's late task is in %q on Anna's device, want Summer", got.Project)
	}
	if got, _ := anna.task(t, private.ID); got.Project != "Trip" {
		t.Errorf("Anna's own Trip task moved to %q", got.Project)
	}
	if anna.cfg.keepsOutOfSync(&private) {
		t.Error("Anna's own Trip is kept from the sync server")
	}
	mark.sync(t, "Trip")
	if got, _ := mark.task(t, late.ID); got.Project != "Summer" {
		t.Errorf("Mark's task is in %q after his pass, want Summer", got.Project)
	}
}

// A subtask put in a shared project while its parent stays out becomes a
// task of its own: to the others it would be a subtask of nothing.
func TestASubtaskSharedWithoutItsParentStandsAlone(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	parent := todo.New("Plan the holiday")
	child, kept := tripTask("Book the ferry"), tripTask("Pick seats")
	child.ParentID = parent.ID
	trip := tripTask("Rent a car")
	kept.ParentID = trip.ID
	anna.save(t, s0, parent, child, trip, kept)
	p := anna.share(t, "Trip", folder)
	if res := anna.sync(t, "Trip"); !res.changed {
		t.Error("the pass did not report the detached subtask")
	}
	if got, _ := anna.task(t, child.ID); got.ParentID != "" {
		t.Errorf("the shared subtask still hangs under %q", got.ParentID)
	}
	if got, _ := anna.task(t, kept.ID); got.ParentID != trip.ID {
		t.Error("a subtask whose parent is in the project was detached")
	}
	if data, _ := os.ReadFile(p.File); strings.Contains(string(data), parent.ID) {
		t.Error("the file still names the private parent")
	}
	mark.join(t, p.File)
	mark.sync(t, "Trip")
	if got, ok := mark.task(t, child.ID); !ok || got.ParentID != "" {
		t.Errorf("Mark got %+v, want the task on its own", got)
	}
}

// A subtask Mark filed in a project of his own stays when its shared parent
// leaves his device, by a move out or by his leaving, as a task of its own:
// under a parent the store no longer holds, no list would show it.
func TestASubtaskOutsideTheProjectOutlivesItsSharedParent(t *testing.T) {
	setTestHome(t, t.TempDir())
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	ferry, gift := tripTask("Book the ferry"), tripTask("Mark's birthday present")
	anna.save(t, s0, ferry, gift)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")

	tickets, wrap := todo.New("Print the tickets"), todo.New("Buy wrapping paper")
	tickets.ParentID, tickets.Project = ferry.ID, "Home"
	wrap.ParentID, wrap.Project = gift.ID, "Home"
	mark.save(t, s0.Add(time.Minute), tickets, wrap)

	moved, _ := anna.task(t, gift.ID)
	moved.Project = "Private"
	anna.save(t, s0.Add(2*time.Minute), moved)
	anna.sync(t, "Trip")
	mark.sync(t, "Trip")
	if _, ok := mark.task(t, gift.ID); ok {
		t.Fatal("the task moved out is still on Mark's device")
	}
	if got, ok := mark.task(t, wrap.ID); !ok || got.ParentID != "" || got.Project != "Home" {
		t.Errorf("after the move out Mark's subtask is %+v, want it on its own in Home", got)
	}

	if _, _, err := leaveShared(mark.h, &mark.cfg, "Trip", rank.Biases{}, mark.by); err != nil {
		t.Fatal(err)
	}
	if got, ok := mark.task(t, tickets.ID); !ok || got.ParentID != "" || got.Project != "Home" {
		t.Errorf("after the leave Mark's subtask is %+v, want it on its own in Home", got)
	}
}

// A folder whose name holds a pattern character is a name like any other:
// the project in it can be joined, and its conflict copies are found.
func TestASharedFolderNameIsNotAPattern(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "Family [shared]")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	p := anna.share(t, "Trip", folder)
	if q := mark.join(t, folder); q.ID != p.ID {
		t.Errorf("joining the folder joined %+v, want %q", q, p.Name)
	}
	copied := filepath.Join(folder, "Trip (1).tjek")
	data, _ := os.ReadFile(p.File)
	if err := os.WriteFile(copied, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sharedConflictCopies(p.File); !slices.Equal(got, []string{copied}) {
		t.Errorf("copies %q, want %q", got, copied)
	}
}

// A rename made elsewhere onto a name Mark uses for a project of his own
// moves his project aside rather than into the shared one: taking the name
// as it is would hand his tasks to everyone.
func TestARenameOntoAnOwnProjectMovesItAside(t *testing.T) {
	setTestHome(t, t.TempDir())
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	ferry := tripTask("Book the ferry")
	anna.save(t, s0, ferry)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")
	diary, taken := todo.New("Mark's private diary"), todo.New("Fix the gutter")
	diary.Project, taken.Project = "Home", "Home (2)"
	mark.save(t, s0, diary, taken)

	save := func(sharedConfig) error { return nil }
	if _, err := renameShared(anna.h, &anna.cfg, "Trip", "Home", rank.Biases{}, anna.by, save); err != nil {
		t.Fatal(err)
	}
	if res := mark.sync(t, "Trip"); res.movedAside != "Home (3)" || res.project.Name != "Home" {
		t.Errorf("Mark's pass: %+v, want the rename taken up and his Home moved to Home (3)", res)
	}
	if data, _ := os.ReadFile(p.File); strings.Contains(string(data), "diary") {
		t.Fatal("Mark's own Home task went into the shared file")
	}
	if got, _ := mark.task(t, diary.ID); got.Project != "Home (3)" {
		t.Errorf("Mark's own task is in %q, want Home (3)", got.Project)
	}
	if got, _ := mark.task(t, ferry.ID); got.Project != "Home" {
		t.Errorf("the shared task is in %q, want Home", got.Project)
	}
	if mark.sync(t, "Home").wrote || anna.sync(t, "Home").wrote {
		t.Error("the devices still rewrite the file after the rename")
	}
}

// The app follows a pass that moved its own project aside for a rename: the
// own tasks first, so the shared ones then take the name alone.
func TestTheAppFollowsAProjectMovedAside(t *testing.T) {
	setTestHome(t, t.TempDir())
	m := newTestModel()
	ferry, diary := tripTask("Book the ferry"), todo.New("Mark's private diary")
	diary.Project = "Home"
	m.Store.add(ferry)
	m.Store.add(diary)
	m.refreshCaches()
	next, _ := m.handleSharedDone(sharedDoneMsg{sharedPass: sharedPass{
		changed: true,
		renames: map[string]string{"Trip": "Home"},
		aside:   map[string]string{"Home": "Home (2)"},
	}})
	m = next.(model)
	if got := m.get(ferry.ID).Project; got != "Home" {
		t.Errorf("the shared task is in %q, want Home", got)
	}
	if got := m.get(diary.ID).Project; got != "Home (2)" {
		t.Errorf("the own task is in %q, want Home (2)", got)
	}
	if !strings.Contains(m.err, "Home (2)") {
		t.Errorf("message %q does not say where the own project went", m.err)
	}
}

// The deletes `tjek undo` can restore follow a shared project: a rename taken
// up from the file moves them to the new name, and leaving forgets them. A
// restore of a task the leave removed would save it as new, every field
// stamped now, and on a later join that stale copy would win over everyone.
func TestSharedProjectUndoFollowsRenameAndLeave(t *testing.T) {
	setTestHome(t, t.TempDir())
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	ferry, bags := tripTask("Book the ferry"), tripTask("Pack the bags")
	anna.save(t, s0, ferry, bags)
	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	mark.join(t, p.File)
	mark.sync(t, "Trip")

	save := func(sharedConfig) error { return nil }
	if _, err := renameShared(anna.h, &anna.cfg, "Trip", "Summer", rank.Biases{}, anna.by, save); err != nil {
		t.Fatal(err)
	}
	gone, _ := mark.task(t, ferry.ID)
	historySave(t, mark.h, s0.Add(time.Minute), mark.by, nil, ferry.ID)
	recordDeleteUndo(undoEntry{desc: undoDescDeleteTask, ids: []string{ferry.ID}, partial: []todo.Todo{gone}})

	mark.sync(t, "Trip")
	entries, err := loadPersistedUndoEntries()
	if err != nil || len(entries) != 1 || entries[0].partial[0].Project != "Summer" {
		t.Fatalf("after the rename the undo history holds %+v (%v), want the delete under Summer", entries, err)
	}

	if _, _, err := leaveShared(mark.h, &mark.cfg, "Summer", rank.Biases{}, mark.by); err != nil {
		t.Fatal(err)
	}
	if entries, err := loadPersistedUndoEntries(); err != nil || len(entries) != 0 {
		t.Errorf("after leaving the undo history holds %+v (%v), want nothing of the project", entries, err)
	}
}

// A task the pass removed because it left the project elsewhere leaves the
// app too, undo history included: an edit or an undo would save it back as
// new, and put it in the shared project again for everyone.
// What `tjek share` changes in shared.json reaches the running app on its
// next poll: a project joined in a terminal has its timers scoped at once, so
// t on its task cannot stop someone else's, and a rename or a leave made there
// carries the tasks and undo history with it.
func TestTheAppFollowsTjekShareWhileItRuns(t *testing.T) {
	setTestHome(t, t.TempDir())
	m := newTestModel()
	ferry, bags := tripTask("Book the ferry"), tripTask("Pack the bags")
	ferry.StartTimerBy("Anna")
	m.Store.add(ferry)
	m.Store.add(bags)
	m.userName = "Mark"
	m.pushUndo("edit", bags.ID)
	m.refreshCaches()

	// tjek share join in a terminal.
	trip := sharedProject{ID: "trip-id", Name: "Trip", File: "Trip.tjek"}
	if err := saveSharedConfig(sharedConfig{Projects: []sharedProject{trip}}); err != nil {
		t.Fatal(err)
	}
	next, _ := m.handleSharedTick(sharedPollMsg{})
	m = next.(model)
	if _, ok := m.shared.find("Trip"); !ok {
		t.Fatal("the app did not take up the project joined from the shell")
	}
	if m.timerRunning(m.get(ferry.ID)) {
		t.Error("Anna's timer still counts as Mark's")
	}

	// tjek share rename.
	trip.Name, trip.Former = "Summer", []string{"Trip"}
	if err := saveSharedConfig(sharedConfig{Projects: []sharedProject{trip}}); err != nil {
		t.Fatal(err)
	}
	next, _ = m.handleSharedTick(sharedPollMsg{})
	m = next.(model)
	if m.get(bags.ID).Project != "Summer" || m.undoStack[len(m.undoStack)-1].partial[0].Project != "Summer" {
		t.Error("the tasks and undo history did not follow the rename made from the shell")
	}

	// tjek share leave.
	left := sharedConfig{Left: []sharedLeft{{ID: trip.ID, Name: "Summer", Tasks: []string{ferry.ID, bags.ID}}}}
	if err := saveSharedConfig(left); err != nil {
		t.Fatal(err)
	}
	next, _ = m.handleSharedTick(sharedPollMsg{})
	m = next.(model)
	if len(m.shared.Projects) != 0 || len(m.undoStack) != 0 || m.get(bags.ID) != nil {
		t.Errorf("after a leave from the shell: shared %+v, undo %d, task kept %v", m.shared.Projects, len(m.undoStack), m.get(bags.ID) != nil)
	}
}

func TestTheAppForgetsATaskThatLeftASharedProject(t *testing.T) {
	setTestHome(t, t.TempDir())
	m := newTestModel()
	gift, ferry := tripTask("Mark's birthday present"), tripTask("Book the ferry")
	m.Store.add(gift)
	m.Store.add(ferry)
	m.pushUndo("edit", gift.ID)
	m.pushUndo("edit", gift.ID, ferry.ID)
	m.refreshCaches()

	next, _ := m.handleSharedDone(sharedDoneMsg{sharedPass: sharedPass{changed: true, removed: []string{gift.ID}}})
	m = next.(model)
	if m.get(gift.ID) != nil {
		t.Fatal("the removed task is still in the app")
	}
	if len(m.undoStack) != 1 || !slices.Equal(m.undoStack[0].ids, []string{ferry.ID}) || len(m.undoStack[0].partial) != 1 {
		t.Fatalf("undo stack %+v, want only the ferry's part of the second entry", m.undoStack)
	}
}

// A task deleted before its project was shared stays on this device: the
// file carries a deleted task only once it held the task, so the others hear
// of deletes in the project, not of what was thrown away before it.
func TestSharingLeavesEarlierDeletesOut(t *testing.T) {
	folder := t.TempDir()
	anna := newSharer(t, "Anna")
	gone, kept := tripTask("Surprise party for Mark"), tripTask("Book the ferry")
	anna.save(t, s0, gone, kept)
	historySave(t, anna.h, s0.Add(time.Minute), anna.by, nil, gone.ID)

	p := anna.share(t, "Trip", folder)
	anna.sync(t, "Trip")
	if data, _ := os.ReadFile(p.File); strings.Contains(string(data), "Surprise") {
		t.Fatal("a task deleted before sharing went into the file")
	}

	historySave(t, anna.h, s0.Add(2*time.Minute), anna.by, nil, kept.ID)
	anna.sync(t, "Trip")
	f, err := readSharedFile(p.File)
	if err != nil || len(f.Tasks) != 1 || f.Tasks[0].ID != kept.ID || !f.Tasks[0].Deleted {
		t.Fatalf("after deleting a shared task the file holds %+v (%v), want its tombstone", f.Tasks, err)
	}
}

// The sync server neither gets nor gives a shared project's tasks, nor the
// ones this device removed by leaving: those travel through the file.
func TestSyncServerSkipsSharedProjects(t *testing.T) {
	shared, left, mine := tripTask("In the shared project"), todo.New("Removed by leaving"), todo.New("Private")
	c := sharedConfig{
		Projects: []sharedProject{{ID: "trip", Name: "Trip"}},
		Left:     []sharedLeft{{ID: "work", Name: "Work", Tasks: []string{left.ID}}},
	}
	got := c.withoutShared([]todo.Todo{shared, left, mine})
	if len(got) != 1 || got[0].ID != mine.ID {
		t.Fatalf("kept %d task(s), want only the private one", len(got))
	}
}

// A device that serves sync to its others and shares a project keeps the
// project out of the sync from its side too: a client neither receives its
// tasks nor gets to change them through the server.
func TestASyncServerThatSharesKeepsTheProjectOut(t *testing.T) {
	setTestHome(t, t.TempDir())
	h := openTestDB(t)
	shared, mine := tripTask("Book the ferry"), todo.New("Post office")
	historySave(t, h, s0, editor{name: "Mark"}, []todo.Todo{shared, mine})
	if err := saveSharedConfig(sharedConfig{Projects: []sharedProject{{ID: "trip", Name: "Trip"}}}); err != nil {
		t.Fatal(err)
	}
	srv := &tasksync.Server{Token: "tok", Store: dbStore{h: h}}

	sneaky := shared
	sneaky.Title = "Changed through the server"
	sneaky.ModifiedAt, sneaky.Stamps = s0.Add(time.Hour), nil
	got, err := srv.Sync([]todo.Todo{sneaky})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != mine.ID {
		t.Errorf("the server answered with %d task(s), want only the one outside the shared project", len(got))
	}
	all, _ := loadTodosForSync(h)
	for _, x := range all {
		if x.ID == shared.ID && x.Title != "Book the ferry" {
			t.Errorf("a client changed the shared task through the server: %q", x.Title)
		}
	}
}

// Sharing and joining refuse what would go wrong later: joining twice, a
// missing folder, a folder holding more than one project to choose from, a
// file of a newer format; and a folder that already holds the project's file
// joins it rather than starting another.
func TestSharedFileGuards(t *testing.T) {
	folder := t.TempDir()
	anna, mark := newSharer(t, "Anna"), newSharer(t, "Mark")
	p := anna.share(t, "Trip", folder)
	if q, err := startSharing(&mark.cfg, "Trip", folder); err != nil || q.ID != p.ID {
		t.Errorf("sharing the folder's own project should join it: %+v, %v", q, err)
	}
	if _, err := joinShared(&mark.cfg, p.File); err == nil {
		t.Error("joining a project twice was accepted")
	}
	if _, err := startSharing(&anna.cfg, "Work", filepath.Join(folder, "missing")); err == nil {
		t.Error("a folder that does not exist was accepted")
	}
	anna.share(t, "Work", folder)
	carl := newSharer(t, "Carl")
	if _, err := joinShared(&carl.cfg, folder); err == nil {
		t.Error("a folder with two projects was joined without saying which")
	}
	if q := carl.join(t, filepath.Join(folder, "Work.tjek")); q.Name != "Work" {
		t.Errorf("joined %q, want Work", q.Name)
	}

	newer := `{"format": 99, "id": "` + p.ID + `", "name": "Trip", "tasks": []}`
	if err := os.WriteFile(p.File, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := syncShared(anna.h, p, rank.Biases{}, anna.by, nil); err == nil || !strings.Contains(err.Error(), "newer tjek") {
		t.Errorf("a file from a newer format: %v, want it refused", err)
	}
}

// A project's file name is its name, less what some system cannot hold.
func TestSharedFileName(t *testing.T) {
	for name, want := range map[string]string{
		"Trip":         "Trip.tjek",
		"Work: Q3/Q4?": "Work- Q3-Q4-.tjek",
		" .. ":         "project.tjek",
	} {
		if got := sharedFileName(name); got != want {
			t.Errorf("sharedFileName(%q) = %q, want %q", name, got, want)
		}
	}
}

// The copies each cloud service makes are found, and neither the file
// itself nor another project whose name merely starts the same.
func TestSharedConflictCopiesAreRecognised(t *testing.T) {
	folder := t.TempDir()
	for _, name := range []string{
		"Trip.tjek", "Trip-LAPTOP.tjek", "Trip (Mark's conflicted copy 2026-09-29).tjek",
		"Trip (1).tjek", "Tripod.tjek", "Trip.tjek.bak",
	} {
		if err := os.WriteFile(filepath.Join(folder, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, c := range sharedConflictCopies(filepath.Join(folder, "Trip.tjek")) {
		got = append(got, filepath.Base(c))
	}
	want := "Trip (1).tjek|Trip (Mark's conflicted copy 2026-09-29).tjek|Trip-LAPTOP.tjek"
	if strings.Join(got, "|") != want {
		t.Errorf("copies %q, want %q", got, want)
	}
}

// A pass with nothing new leaves the file alone: every write is an upload.
func TestUnchangedSharedProjectIsNotRewritten(t *testing.T) {
	folder := t.TempDir()
	anna := newSharer(t, "Anna")
	anna.save(t, s0, tripTask("Book the ferry"))
	anna.share(t, "Trip", folder)
	if !anna.sync(t, "Trip").wrote {
		t.Fatal("the first pass wrote nothing")
	}
	if anna.sync(t, "Trip").wrote {
		t.Error("an unchanged project rewrote its file")
	}
}

// writeSharedFile puts an empty shared project's file at path, as sharing
// from another device would have.
func writeSharedFile(t *testing.T, path, id, name string) {
	t.Helper()
	data, err := encodeSharedFile(sharedFile{Format: sharedFormat, ID: id, Name: name, Tasks: []todo.Todo{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// `tjek share join` asks before handing over tasks this device already files
// under the project's name, and --merge goes ahead.
func TestCLIJoinAsksBeforeMergingALocalProject(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Trip.tjek")
	setTestHome(t, t.TempDir())
	testStore(t)
	t.Setenv("TJEK_AUTHOR", "")
	captureStdout(t, func() { cliAdd([]string{"Mine already", "--project", "Trip"}) })
	writeSharedFile(t, file, "trip-id", "Trip")

	var code int
	captureStderr(t, func() { code = cliShare([]string{"join", file}) })
	if code != 2 {
		t.Fatalf("join over a local project of the same name: exit %d, want 2", code)
	}
	if c, _ := loadSharedConfig(); len(c.Projects) != 0 {
		t.Fatal("the refused join was recorded anyway")
	}
	captureStdout(t, func() { code = cliShare([]string{"join", file, "--merge"}) })
	if code != 0 {
		t.Fatalf("join --merge: exit %d", code)
	}
	f, err := readSharedFile(file)
	if err != nil || len(f.Tasks) != 1 {
		t.Errorf("the file holds %d task(s) after joining with --merge (%v), want the local one", len(f.Tasks), err)
	}
	captureStdout(t, func() { code = cliShare([]string{"leave", "Trip"}) })
	if c, _ := loadSharedConfig(); code != 0 || len(c.Projects) != 0 {
		t.Errorf("leave: exit %d, still shared: %+v", code, c.Projects)
	}
}

// S on a Projects row shares it in a file; S on a shared one asks, and y
// leaves it and removes its tasks, which no undo brings back. The row carries
// the shared mark between.
func TestScriptShareAndLeaveFromTheProjectsTab(t *testing.T) {
	folder := t.TempDir()
	setTestHome(t, t.TempDir())
	testStore(t)
	captureStdout(t, func() { cliAdd([]string{"Book the ferry", "--project", "Trip"}) })
	m := initialModel(newSQLiteRepo())
	m.termWidth, m.termHeight = 120, 40
	m.tab = tabProjects
	m.refreshCaches()

	m = sendKey(t, m, "S")
	if m.mode != modeShareFolder {
		t.Fatalf("S: mode = %v, want modeShareFolder", m.mode)
	}
	m = script(t, m, folder, "enter")
	if m.mode != modeNormal {
		t.Fatalf("after enter: mode = %v (%s)", m.mode, m.err)
	}
	if _, ok := m.shared.find("Trip"); !ok {
		t.Fatal("the project is not shared after enter")
	}
	if c, _ := loadSharedConfig(); len(c.Projects) != 1 {
		t.Fatal("shared.json does not record the project")
	}
	if _, err := os.Stat(filepath.Join(folder, "Trip.tjek")); err != nil {
		t.Fatalf("no Trip.tjek in the folder: %v", err)
	}
	if !strings.Contains(ansi.Strip(m.View()), "Trip"+sharedMark) {
		t.Error("the Projects row does not show the shared mark")
	}

	m.pushUndo("edit", m.allTodos()[0].ID)

	m = sendKey(t, m, "S")
	if m.mode != modeConfirm {
		t.Fatalf("S on a shared project: mode = %v, want the leave prompt", m.mode)
	}
	m, cmd := sendKeyCmd(t, m, keyMsgFor("y"))
	if _, ok := m.shared.find("Trip"); ok {
		t.Fatal("still shared after y")
	}
	for _, msg := range runCmd(cmd) {
		if r, ok := msg.(reloadedMsg); ok {
			m, _ = send(t, m, r)
		}
	}
	if n := len(m.allTodos()); n != 0 {
		t.Errorf("%d task(s) left after leaving, want the project's removed", n)
	}
	m = sendKey(t, m, "u")
	if n := len(m.allTodos()); n != 0 {
		t.Errorf("u after leaving brought back %d task(s)", n)
	}
}

// r on a shared project renames it for everyone after asking: the tasks, the
// file and shared.json take the new name, and the undo history follows, so an
// undo cannot put a task back under the old name. x refuses, as taking the
// project off every task would move them all out for everyone, and no other
// project can be renamed onto a shared one.
func TestScriptRenameASharedProject(t *testing.T) {
	folder := t.TempDir()
	setTestHome(t, t.TempDir())
	testStore(t)
	captureStdout(t, func() {
		cliAdd([]string{"Book the ferry", "--project", "Trip"})
		cliAdd([]string{"Paint the fence", "--project", "Home"})
	})
	m := initialModel(newSQLiteRepo())
	m.termWidth, m.termHeight = 120, 40
	m.tab = tabProjects
	m.refreshCaches()
	m.projectCursor = slices.Index(m.allProjectsForList(), "Trip")
	m = sendKey(t, m, "S")
	m = script(t, m, folder, "enter")
	if _, ok := m.shared.find("Trip"); !ok {
		t.Fatalf("not shared: %s", m.err)
	}
	var ferry string
	for _, x := range m.allTodos() {
		if x.Title == "Book the ferry" {
			ferry = x.ID
		}
	}
	m.pushUndo("edit", ferry)

	m.projectCursor = slices.Index(m.allProjectsForList(), "Trip")
	m = sendKey(t, m, "x")
	if m.mode != modeNormal || !strings.Contains(m.err, "leave it before removing it") {
		t.Errorf("x on the shared project: mode %v, message %q; want it refused", m.mode, m.err)
	}

	m = sendKey(t, m, "r")
	m.textInput.SetValue("Summer")
	m = sendKey(t, m, "enter")
	if m.mode != modeConfirm || !strings.Contains(m.confirmMsg, "for everyone") {
		t.Fatalf("enter on the new name: mode %v, prompt %q; want the question", m.mode, m.confirmMsg)
	}
	m = sendKey(t, m, "y")
	if _, ok := m.shared.find("Summer"); !ok {
		t.Fatalf("shared.json in memory still says %+v (%s)", m.shared.Projects, m.err)
	}
	if c, _ := loadSharedConfig(); len(c.Projects) != 1 || c.Projects[0].Name != "Summer" || !slices.Equal(c.Projects[0].Former, []string{"Trip"}) {
		t.Errorf("shared.json on disk: %+v", c.Projects)
	}
	if got := m.get(ferry); got.Project != "Summer" {
		t.Errorf("the task is in %q, want Summer", got.Project)
	}
	f, err := readSharedFile(filepath.Join(folder, "Trip.tjek"))
	if err != nil || f.Name != "Summer" || len(f.Tasks) != 1 || f.Tasks[0].Project != "Summer" {
		t.Errorf("the file: %+v (%v)", f, err)
	}
	m = sendKey(t, m, "u")
	if got := m.get(ferry); got == nil || got.Project != "Summer" {
		t.Errorf("an undo from before the rename put the task in %+v", got)
	}

	m.projectCursor = slices.Index(m.allProjectsForList(), "Home")
	m = sendKey(t, m, "r")
	m.textInput.SetValue("Summer")
	m = sendKey(t, m, "enter")
	if !strings.Contains(m.err, "cannot move tasks into it") {
		t.Errorf("renaming Home onto the shared name: message %q, want it refused", m.err)
	}
}

// Settings → Join a project asks before sharing tasks already filed under
// the project's name, and joins on y.
// A rename's pass merges the file too, so the app's copies of the project's
// tasks predate the others' edits it brought in. Following the rename must
// not save those copies, or their stale fields would be stamped as new edits
// and undo the others' work for everyone: whether the rename is made here or
// taken up from the file.
func TestARenameInTheAppKeepsTheOthersEdits(t *testing.T) {
	setup := func(t *testing.T) (model, *sharer, string) {
		folder := t.TempDir()
		setTestHome(t, t.TempDir())
		testStore(t)
		captureStdout(t, func() { cliAdd([]string{"Book the ferry", "--project", "Trip"}) })
		m := initialModel(newSQLiteRepo())
		m.termWidth, m.termHeight = 120, 40
		m.tab = tabProjects
		m.refreshCaches()
		m.projectCursor = slices.Index(m.allProjectsForList(), "Trip")
		m = sendKey(t, m, "S")
		m = script(t, m, folder, "enter")
		p, ok := m.shared.find("Trip")
		if !ok {
			t.Fatalf("not shared: %s", m.err)
		}
		if _, err := syncAllShared(db, rank.Biases{}, m.editor()); err != nil {
			t.Fatal(err)
		}
		ferry := m.allTodos()[0].ID
		anna := newSharer(t, "Anna")
		anna.join(t, p.File)
		anna.sync(t, "Trip")
		got, _ := anna.task(t, ferry)
		got.Title = "Book the 9:00 ferry"
		anna.save(t, time.Now(), got)
		return m, anna, ferry
	}
	check := func(t *testing.T, m model, anna *sharer, ferry string) {
		t.Helper()
		m.flushPendingWrites()
		anna.sync(t, anna.cfg.Projects[0].Name)
		for _, s := range []*sharer{{h: db, by: m.editor()}, anna} {
			if got, _ := s.task(t, ferry); got.Title != "Book the 9:00 ferry" || got.Project != "Summer" {
				t.Errorf("%s's store holds %q in %q, want Anna's title in Summer", s.by.name, got.Title, got.Project)
			}
		}
	}

	t.Run("renamed here", func(t *testing.T) {
		m, anna, ferry := setup(t)
		anna.sync(t, "Trip")
		m.projectCursor = slices.Index(m.allProjectsForList(), "Trip")
		m = sendKey(t, m, "r")
		m.textInput.SetValue("Summer")
		m = sendKey(t, m, "enter")
		m = sendKey(t, m, "y")
		check(t, m, anna, ferry)
	})

	t.Run("renamed elsewhere", func(t *testing.T) {
		m, anna, ferry := setup(t)
		if _, err := renameShared(anna.h, &anna.cfg, "Trip", "Summer", rank.Biases{}, anna.by, func(sharedConfig) error { return nil }); err != nil {
			t.Fatal(err)
		}
		pass, err := syncAllShared(db, rank.Biases{}, m.editor())
		if err != nil {
			t.Fatal(err)
		}
		next, _ := m.handleSharedDone(sharedDoneMsg{sharedPass: pass})
		m = next.(model)
		check(t, m, anna, ferry)
	})
}

func TestScriptJoinFromSettingsAsksFirst(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Trip.tjek")
	writeSharedFile(t, file, "trip-id", "Trip")
	mine := tripTask("Mine already")
	m := settingsModel(t)
	m.Store.add(mine)
	m.refreshCaches()

	m = openSetting(t, m, settingShareJoin)
	if m.mode != modeShareJoin {
		t.Fatalf("mode = %v, want modeShareJoin", m.mode)
	}
	m = script(t, m, file, "enter")
	if m.mode != modeConfirm {
		t.Fatalf("join over a local project: mode = %v, want the question", m.mode)
	}
	m = sendKey(t, m, "y")
	if _, ok := m.shared.find("Trip"); !ok {
		t.Fatal("not joined after y")
	}
}
