package app

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
	tea "github.com/charmbracelet/bubbletea"
)

// recordingRepo is a fakeRepo that keeps what each Save was handed, and can
// be told to fail.
type recordingRepo struct {
	fakeRepo
	saves []recordedSave
	err   error
}

type recordedSave struct {
	dirty      []string
	tombstones map[string]time.Time
}

func (r *recordingRepo) Save(dirty []*todo.Todo, tombstones map[string]time.Time) error {
	rec := recordedSave{tombstones: tombstones}
	for _, t := range dirty {
		rec.dirty = append(rec.dirty, t.ID)
	}
	r.saves = append(r.saves, rec)
	return r.err
}

func (r *recordingRepo) SaveOnto(dirty []*todo.Todo, _ map[string]*todo.Todo, tombstones map[string]time.Time) error {
	return r.Save(dirty, tombstones)
}

func modelWithRecordingRepo(t *testing.T, tasks ...todo.Todo) (model, *recordingRepo) {
	t.Helper()
	m := modelWithTasks(t, tasks...)
	repo := &recordingRepo{fakeRepo: fakeRepo{todos: tasks}}
	m.repo = repo
	return m, repo
}

// runCmd runs cmd the way Bubble Tea would, batches included, and returns
// the messages it produced.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmd(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func send(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

// An edit is written by the save tick that follows it: the key schedules the
// tick, and the tick hands the edited task to the repository off the loop.
// A tick with nothing pending writes nothing.
func TestSaveTickWritesWhatTheKeyChanged(t *testing.T) {
	task := todo.New("save me")
	m, repo := modelWithRecordingRepo(t, task)

	m = sendKey(t, m, "p")
	if !m.saveScheduled || !m.savePending {
		t.Fatalf("an edit should schedule a save: scheduled=%v pending=%v", m.saveScheduled, m.savePending)
	}

	m, cmd := send(t, m, saveTickMsg{})
	if m.saveScheduled || m.savePending {
		t.Errorf("the tick should consume the schedule: scheduled=%v pending=%v", m.saveScheduled, m.savePending)
	}
	if len(repo.saves) != 0 {
		t.Fatal("the save ran on the Update loop instead of in its command")
	}
	var done bool
	for _, msg := range runCmd(cmd) {
		if _, ok := msg.(saveDoneMsg); ok {
			done = true
		}
	}
	if !done || len(repo.saves) != 1 {
		t.Fatalf("saves = %d, done = %v; want one save reporting done", len(repo.saves), done)
	}
	if got := repo.saves[0].dirty; len(got) != 1 || got[0] != task.ID {
		t.Errorf("saved %v, want only the edited task %s", got, task.ID)
	}

	_, cmd = send(t, m, saveTickMsg{})
	runCmd(cmd)
	if len(repo.saves) != 1 {
		t.Errorf("a tick with nothing pending saved again (%d saves)", len(repo.saves))
	}
}

// Starting a timer stops the one running on another task, and the save
// writes that stop too: left out, the store would keep the other timer
// running, and a restart or another device would count its time on.
func TestStartingATimerSavesTheOneItStopped(t *testing.T) {
	a, b := todo.New("first"), todo.New("second")
	m, repo := modelWithRecordingRepo(t, a, b)
	first := m.currentTodo().ID
	m = sendKey(t, m, "t")
	m, cmd := send(t, m, saveTickMsg{})
	for _, msg := range runCmd(cmd) {
		m, _ = send(t, m, msg) // the save's end, which lets the next one run
	}
	m = sendKey(t, m, "down")
	m = sendKey(t, m, "t")
	if m.timerRunning(m.get(first)) {
		t.Fatal("setup: the first timer still runs")
	}
	_, cmd = send(t, m, saveTickMsg{})
	runCmd(cmd)
	if len(repo.saves) != 2 || !slices.Contains(repo.saves[1].dirty, first) {
		t.Errorf("saves %+v, want the second to hold the task whose timer stopped", repo.saves)
	}
}

// Closing a recurring task with subtasks spawns its next instance with fresh
// copies of them, and the save writes the copies too: left out, they lived
// in memory alone and were gone after a restart.
func TestClosingARecurringTaskSavesItsNextChecklist(t *testing.T) {
	review := todo.New("Weekly review")
	review.Recurrence = "weekly"
	inbox := todo.NewSubtask("Empty the inbox", review.ID)
	inbox.Status = todo.Done
	m, repo := modelWithRecordingRepo(t, review, inbox)
	m = sendKey(t, m, "d")
	m, cmd := send(t, m, saveTickMsg{})
	runCmd(cmd)
	if len(repo.saves) != 1 {
		t.Fatalf("saves %+v, want one", repo.saves)
	}
	var clones int
	for _, x := range m.allTodos() {
		if x.ParentID == "" || x.ParentID == review.ID {
			continue
		}
		clones++
		if !slices.Contains(repo.saves[0].dirty, x.ID) {
			t.Errorf("the next instance's %q was not saved", x.Title)
		}
	}
	if clones != 1 {
		t.Errorf("the next instance has %d subtasks, want the one copy", clones)
	}
}

// A delete reaches the repository as a tombstone, which is what makes it sync.
func TestSaveTickCarriesTheTombstone(t *testing.T) {
	keep, gone := todo.New("keep"), todo.New("gone")
	m, repo := modelWithRecordingRepo(t, keep, gone)

	m.Store.remove(gone.ID)
	m.Store.markTombstone(gone.ID)
	m.savePending = true

	_, cmd := send(t, m, saveTickMsg{})
	runCmd(cmd)
	if len(repo.saves) != 1 {
		t.Fatalf("saves = %d, want 1", len(repo.saves))
	}
	if _, ok := repo.saves[0].tombstones[gone.ID]; !ok {
		t.Errorf("tombstones = %v, want %s", repo.saves[0].tombstones, gone.ID)
	}
}

// A save that fails says so on screen rather than losing the edit quietly.
func TestSaveTickReportsAFailedSave(t *testing.T) {
	m, repo := modelWithRecordingRepo(t, todo.New("unlucky"))
	repo.err = errors.New("disk full")

	m = sendKey(t, m, "p")
	m, cmd := send(t, m, saveTickMsg{})
	var failed tea.Msg
	for _, msg := range runCmd(cmd) {
		if _, ok := msg.(saveErrMsg); ok {
			failed = msg
		}
	}
	if failed == nil {
		t.Fatal("a failed save produced no saveErrMsg")
	}
	m, _ = send(t, m, failed)
	if m.err == "" {
		t.Error("a failed save left nothing on screen")
	}
}

// A running timer's last-seen stamp is refreshed at most once a minute, so
// the stale-timer recovery never takes a live timer for an abandoned one. With
// no timer running the tick stops re-arming itself.
func TestTimerTickHeartbeatsOncePerMinute(t *testing.T) {
	task := todo.New("tracked")
	task.StartTimer()
	m := modelWithTasks(t, task)
	m.timerTickOn = true

	m, cmd := send(t, m, timerTickMsg{})
	if cmd == nil || m.lastTimerHeartbeat.IsZero() {
		t.Fatal("the first tick with a timer running should heartbeat and re-arm")
	}
	first := m.lastTimerHeartbeat
	if e := m.get(task.ID).RunningEntry(); e == nil || !e.LastSeen.Equal(first) {
		t.Errorf("the running entry's LastSeen was not stamped with the heartbeat")
	}

	m, cmd = send(t, m, timerTickMsg{})
	if cmd == nil || !m.lastTimerHeartbeat.Equal(first) {
		t.Error("a second tick within the minute should re-arm without another heartbeat")
	}

	m.stopTimer(task.ID)
	m, cmd = send(t, m, timerTickMsg{})
	if cmd != nil || m.timerTickOn {
		t.Errorf("with no timer running the tick should stop: cmd=%v on=%v", cmd != nil, m.timerTickOn)
	}
}
