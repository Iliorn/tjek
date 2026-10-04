package app

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
)

// startWatcherOrSkip starts a watcher, skipping the test when the host can't
// initialize inotify (e.g. fs.inotify.max_user_instances exhausted on a busy
// box) — that's an environment limit, not a watcher bug, so a hard failure
// would just be noise. Returns the cleanup func to defer.
func startWatcherOrSkip(t *testing.T, state *watcherState, dir string) func() {
	t.Helper()
	cleanup, err := startWatcher(state, dir)
	if err != nil {
		if strings.Contains(err.Error(), "inotify") {
			t.Skipf("inotify unavailable on this host: %v", err)
		}
		t.Fatalf("startWatcher: %v", err)
	}
	return cleanup
}

// TestShouldReloadNowWhileTypingDefers covers the modal-mode rule: a fs
// event arriving while the user is mid-edit must not clobber the input.
// shouldReloadNow returns false AND records pendingExternalReload so the
// Update wrapper can drain it on mode exit.
func TestShouldReloadNowWhileTypingDefers(t *testing.T) {
	ws := newWatcherState()

	if ws.shouldReloadNow(modeInput) {
		t.Error("modeInput should defer reload, got true")
	}
	if !ws.drainPending() {
		t.Error("pending reload flag should be set after deferred event")
	}
	if ws.drainPending() {
		t.Error("drainPending should clear the flag — second call must return false")
	}
}

// An idle TUI looks into a signal at once; whether it reads the store is
// unseenWrite's to say.
func TestShouldReloadNowHappyPath(t *testing.T) {
	ws := newWatcherState()
	if !ws.shouldReloadNow(modeNormal) {
		t.Error("an idle TUI should look into the signal immediately")
	}
	if ws.drainPending() {
		t.Error("looking into it now must not also queue it for later")
	}
}

// TestStartWatcherFiresOnFileChange exercises the real fsnotify path: spin
// up a watcher on a temp dir, write to tasks.db, and confirm a dbChangedMsg
// arrives on the channel within a generous timeout. This validates the
// directory-watch + filename-filter + debounce wiring end-to-end without
// running the full TUI.
func TestStartWatcherFiresOnFileChange(t *testing.T) {
	dir := t.TempDir()
	state := newWatcherState()

	defer startWatcherOrSkip(t, state, dir)()

	// Write to tasks.db — this is the file the watcher is filtered to.
	dbPath := dir + "/tasks.db"
	if err := os.WriteFile(dbPath, []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Wait up to 2s for the debounce (200ms) + a generous buffer for slow CI.
	select {
	case <-state.ch:
		// got it
	case <-time.After(2 * time.Second):
		t.Fatal("expected dbChangedMsg within 2s of writing tasks.db, got nothing")
	}
}

// TestStartWatcherIgnoresUnrelatedFiles confirms the filename filter: a
// write to some-other-file.txt in the watched directory must not fire the
// channel, only writes to tasks.db / tasks.db-wal do.
func TestStartWatcherIgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	state := newWatcherState()

	defer startWatcherOrSkip(t, state, dir)()

	if err := os.WriteFile(dir+"/not-relevant.txt", []byte("noise"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 500ms is well past the debounce — if nothing arrives by then, the
	// filter works.
	select {
	case <-state.ch:
		t.Fatal("watcher fired on an unrelated file write")
	case <-time.After(500 * time.Millisecond):
	}
}

// unseenWrite answers what the watcher must know: whether anything but the
// app's own saves wrote since the last read. Another process moves
// data_version; this process's merges move foreignWrites; the app's own
// saves move neither. Each answer marks what it saw.
func TestUnseenWriteTellsTheAppsOwnWritesApart(t *testing.T) {
	ws := newWatcherState()
	version := int64(7)
	read := func() (int64, error) { return version, nil }

	if !ws.unseenWrite(read) {
		t.Error("before any read, the store counts as unseen")
	}
	if ws.unseenWrite(read) {
		t.Error("nothing wrote, yet a write was reported (the app's own save looks like this)")
	}
	version++
	if !ws.unseenWrite(read) || ws.unseenWrite(read) {
		t.Error("another process's commit was not reported exactly once")
	}
	noteForeignWrite()
	if !ws.unseenWrite(read) || ws.unseenWrite(read) {
		t.Error("a sync merge in this process was not reported exactly once")
	}
	if !ws.unseenWrite(func() (int64, error) { return 0, errors.New("busy") }) {
		t.Error("an unreadable data_version must count as a write")
	}
}

// The real store: a write by another connection moves data_version, a write
// on the app's own does not, so a save landing next to another process's
// commit can no longer hide it.
func TestDataVersionSeesOnlyOtherConnections(t *testing.T) {
	setTestHome(t, t.TempDir())
	testStore(t)
	ws := newWatcherState()
	ws.unseenWrite(storeDataVersion)

	if err := newSQLiteRepo().Save([]*todo.Todo{ptr(todo.New("mine"))}, nil); err != nil {
		t.Fatal(err)
	}
	if ws.unseenWrite(storeDataVersion) {
		t.Error("the app's own save was taken for someone else's")
	}

	other, err := openStoreAt(dbPath())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	theirs := todo.New("theirs")
	if err := saveStamped(other, []*todo.Todo{&theirs}, nil, func(*todo.Todo) float64 { return 0 }, time.Now(), editor{name: "cli"}); err != nil {
		t.Fatal(err)
	}
	if !ws.unseenWrite(storeDataVersion) {
		t.Error("another connection's commit was not seen")
	}
}

func ptr[T any](v T) *T { return &v }
