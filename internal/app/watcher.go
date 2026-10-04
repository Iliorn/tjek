package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Iliorn/tjek/tasksync"
	"github.com/Iliorn/tjek/todo"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"
)

// watcher.go bridges fs events in the data directory into Bubble Tea messages so the
// TUI reloads when the CLI (or another process) mutates the database. The
// design choices are constrained by SQLite WAL semantics and Bubble Tea's
// goroutine model:
//
//   - We watch the *directory*, not just tasks.db. WAL writes touch
//     tasks.db-wal first and only periodically flush into tasks.db, so a
//     file-level watch on tasks.db alone misses most events.
//
//   - One goroutine reads fsnotify events, debounces them (200ms quiet
//     period coalesces a burst of WAL writes into a single reload signal),
//     and posts to a channel.
//
//   - The Update loop returns a tea.Cmd that reads one signal from that
//     channel — when it arrives, Update reloads via the Repository and
//     re-arms by returning the same Cmd. This is the canonical Bubble Tea
//     pattern for long-lived goroutines.
//
//   - Whose write: every write the TUI makes itself wakes the watcher too,
//     and must not cost a reload. Before reading the store, the reload asks
//     whether anything wrote that memory does not hold (unseenWrite): another
//     process's commit, which moves SQLite's data_version (it counts commits
//     by other connections, and this process has one), or a write of this
//     process's own that did not come from memory (foreignWrites: a sync
//     merge, a shared project's pass, a client's push to the in-process
//     server). The answer is exact, so a write that lands just after the
//     TUI's own save is never mistaken for it.
//
//   - Modal suppression: when m.mode != modeNormal (text input, confirm
//     prompt, etc.) the reload is deferred — clobbering an in-flight edit
//     would be jarring. The watcher sets pendingExternalReload; the next
//     return to modeNormal triggers the deferred reload.

const watcherDebounceWindow = 200 * time.Millisecond

// foreignWrites counts the writes this process makes to the store that did
// not come from the app's memory: a sync merge, a shared project's pass, a
// client's push to the in-process server. They run on goroutines with no
// model to tell, and share the app's connection, so data_version does not
// see them; the watcher compares this count instead (unseenWrite).
var foreignWrites atomic.Uint64

// noteForeignWrite records a committed write of that kind.
func noteForeignWrite() { foreignWrites.Add(1) }

// storeDataVersion is SQLite's data_version on the app's connection, which
// moves when another connection, so another process, commits.
func storeDataVersion() (int64, error) {
	if db == nil {
		return 0, errors.New("no store open")
	}
	var v int64
	err := db.QueryRow(`PRAGMA data_version`).Scan(&v)
	return v, err
}

// watchSignal is the single-bit "the DB changed, you should consider reloading"
// signal posted by the watcher goroutine. We use a 1-buffered channel and
// drop subsequent signals while one is pending — there's no useful coalescing
// signal richer than "something happened".
type dbChangedMsg struct{}

// reloadedMsg carries the result of an async repo.Load triggered by a watcher
// event so Update can swap the task set in atomically.
type reloadedMsg struct {
	todos []todo.Todo
	err   error
	// epoch is the model's saveEpoch when the read was started (reloadCmd).
	epoch uint64
	// fingerprint and versions are loadedFingerprint(todos), worked out off
	// the loop; zero and nil when the sender left them to handleReloaded.
	fingerprint uint64
	versions    []uint64
}

// watcherState lives on the model. The mutex protects pendingExternalReload
// and the seen marks, which the Update goroutine and the reload commands
// running off it both touch.
type watcherState struct {
	mu                    sync.Mutex
	ch                    chan dbChangedMsg
	pendingExternalReload bool
	// seenVersion and seenWrites are the data_version and foreignWrites
	// as of the last read of the store (unseenWrite); seen is false until
	// the first.
	seenVersion int64
	seenWrites  uint64
	seen        bool
}

func newWatcherState() *watcherState {
	return &watcherState{ch: make(chan dbChangedMsg, 1)}
}

// shouldReloadNow decides whether a watcher signal should be looked into
// now: not while the user is typing, which would clobber the input. Pure
// given the mode; unit-testable without fsnotify or a real DB.
func (w *watcherState) shouldReloadNow(mode appMode) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if mode != modeNormal {
		// Defer — user is editing. Mark pending; the mode-exit path picks
		// it up.
		w.pendingExternalReload = true
		return false
	}
	return true
}

// unseenWrite reports whether the store holds a write memory has not seen
// since the last call: another process's commit, or one of this process's
// foreign writes. It marks both seen, so a read of the store started after
// it holds everything it reported. A data_version it cannot read counts as a
// write, which costs a read and nothing else. It runs off the loop: the
// query waits for the connection a save may hold.
func (w *watcherState) unseenWrite(version func() (int64, error)) bool {
	writes := foreignWrites.Load()
	v, err := version()
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := err != nil || !w.seen || v != w.seenVersion || writes != w.seenWrites
	w.seenVersion, w.seenWrites, w.seen = v, writes, err == nil
	return changed
}

// drainPending returns true if a deferred reload was queued while the user
// was in a modal mode, and atomically clears the flag.
func (w *watcherState) drainPending() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pendingExternalReload {
		w.pendingExternalReload = false
		return true
	}
	return false
}

// startWatcher spawns the fsnotify goroutine. Returns the dbChangedMsg
// channel for the model to read via waitForDBChange, and a cleanup func that
// stops the watcher. If fsnotify or the directory watch fails, the function
// returns with err — the TUI should continue without live reload rather than
// abort startup.
func startWatcher(state *watcherState, dir string) (cleanup func(), err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}
	// Ensure the directory exists before we try to watch it — fresh installs
	// reach openStore which creates it, but we can be called either before
	// or after depending on test path.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			w.Close()
			return nil, fmt.Errorf("mkdir watch target: %w", err)
		}
	}
	if err := w.Add(dir); err != nil {
		w.Close()
		return nil, fmt.Errorf("watch %s: %w", dir, err)
	}

	stop := make(chan struct{})
	go func() {
		var debounce *time.Timer
		for {
			select {
			case <-stop:
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				// Care about writes to tasks.db / tasks.db-wal only.
				base := filepath.Base(ev.Name)
				if base != "tasks.db" && base != "tasks.db-wal" {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
					continue
				}
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(watcherDebounceWindow, func() {
					select {
					case state.ch <- dbChangedMsg{}:
					default:
						// channel full — a signal is already pending,
						// no point queueing more
					}
				})
			case err, ok := <-w.Errors:
				if !ok {
					return // watcher closed — Errors drained, stop reading it
				}
				// fsnotify error — log and keep going. A spurious error here
				// shouldn't kill the watcher; rare enough that a stderr line
				// (even under a live TUI) beats swallowing it.
				log.Printf("tjek watcher: %v", err)
			}
		}
	}()

	// Idempotent: the model is a value type, so several copies can hold this
	// same func (the quit path and a test cleanup, say) and each will call it.
	// Without the Once, the second call closes an already-closed channel and
	// panics.
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			w.Close()
		})
	}, nil
}

// waitForDBChange is the tea.Cmd that bridges the watcher's channel into the
// Update loop. Returns the next dbChangedMsg from the channel (blocking),
// which Bubble Tea delivers as a regular msg. The Update handler re-arms by
// returning waitForDBChange again.
func waitForDBChange(ch chan dbChangedMsg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// startChangeWatcher watches the storage directory and nudges the hub whenever
// tasks.db changes, so a direct CLI write on the server host (tjek add/done…)
// reaches connected clients in real time — not only client-initiated merges. It
// reuses the same fsnotify plumbing the TUI uses for live reload. Returns a stop
// func; if the watcher can't start the server still works, it just loses
// real-time push for out-of-process writes (client syncs still nudge directly).
func startChangeWatcher(hub *tasksync.Hub, dir string) (stop func(), err error) {
	state := newWatcherState()
	cleanup, err := startWatcher(state, dir)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-state.ch:
				hub.Broadcast()
			}
		}
	}()
	return func() { close(done); cleanup() }, nil
}
