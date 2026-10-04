package app

import (
	"fmt"
	"time"

	"github.com/Iliorn/tjek/todo"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Background messages ───────────────────────────────────────────────────────

// handleBackgroundMsg answers the messages that arrive from ticks, commands
// and watchers rather than from the user, whatever the mode. ok is false for
// anything else, which dispatch then routes by mode.
func (m model) handleBackgroundMsg(msg tea.Msg) (next tea.Model, cmd tea.Cmd, ok bool) {
	switch msg := msg.(type) {
	case clearErrMsg:
		m.err = ""
		m.errKind = toastError
		next = m
	case timerTickMsg:
		next, cmd = m.handleTimerTick()
	case updateDoneMsg:
		if msg.err != nil {
			m.flashError(fmt.Sprintf("Update failed: %v", msg.err))
			m.updateStatus = tr("Update failed")
		} else {
			m.flashSuccess(tr("Updated! Restart tjek to apply."))
			m.updateStatus = tr("Updated; restart to apply")
		}
		next, cmd = m, clearErrAfter()
	case updateCheckMsg:
		next, cmd = m.handleUpdateCheck(msg)
	case saveDoneMsg:
		m.adoptSaved(msg.saved, msg.sent)
		next, cmd = m, tea.Batch(m.sharedSoon(), m.saveFinished())
	case sharedPollMsg, sharedSoonMsg:
		next, cmd = m.handleSharedTick(msg)
	case sharedDoneMsg:
		next, cmd = m.handleSharedDone(msg)
	case syncTickMsg:
		next, cmd = m.handleSyncTick()
	case syncEventMsg:
		next, cmd = m.handleSyncEvent()
	case syncDoneMsg:
		next, cmd = m.handleSyncDone(msg)
	case dayTickMsg:
		next, cmd = m.handleDayTick(msg)
	case exportTickMsg:
		cmd = m.exportTick()
		next = m
	case exportDoneMsg:
		if msg.err != nil {
			m.exportDirty = true // retried with the next change
			m.flashError(fmt.Sprintf(tr("Auto-export failed: %v"), msg.err))
			cmd = clearErrAfter()
		}
		next = m
	case importDoneMsg:
		next, cmd = m.handleImportDone(msg)
	case serverProbeMsg:
		// Only flag "external" when we aren't the one serving in-process.
		m.serverExternal = msg.reachable && m.inprocServer == nil
		next = m
	case saveErrMsg:
		m.flashError(fmt.Sprintf("Error saving tasks: %v", msg.err))
		next, cmd = m, tea.Batch(clearErrAfter(), m.saveFinished())
	case editorFinishedMsg:
		next, cmd = m.handleEditorFinished(msg)
	case saveTickMsg:
		next, cmd = m.handleSaveTick()
	case dbChangedMsg:
		next, cmd = m.handleDBChanged()
	case reloadedMsg:
		next, cmd = m.handleReloaded(msg)
	default:
		return nil, nil, false
	}
	return next, cmd, true
}

func (m model) handleTimerTick() (tea.Model, tea.Cmd) {
	if !m.anyTimerRunning() {
		m.timerTickOn = false
		return m, nil
	}
	// Heartbeat the running timer's last_seen at most once a minute so the
	// stale-timer recoverer never mistakes this live timer for an abandoned
	// one. recordSelfSave keeps the fs watcher from reloading on our own
	// write. The write itself runs as a tea.Cmd — off the Update goroutine —
	// so a busy DB (concurrent sync/CLI write inside busy_timeout) can't
	// freeze the UI for up to 5s.
	if time.Since(m.lastTimerHeartbeat) < time.Minute {
		return m, timerTick()
	}
	m.lastTimerHeartbeat = time.Now()
	// Keep the in-memory entries in step with the DB heartbeat — see
	// stampRunningTimersSeen for why saves depend on this.
	m.stampRunningTimersSeen(m.lastTimerHeartbeat)
	if m.watcher != nil {
		m.watcher.recordSelfSave()
	}
	sc := m.timers
	return m, tea.Batch(timerTick(), func() tea.Msg {
		_ = heartbeatRunningTimers(db, time.Now(), sc)
		return nil
	})
}

func (m model) handleUpdateCheck(msg updateCheckMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.flashError(fmt.Sprintf("Update check failed: %v", msg.err))
		m.updateStatus = tr("Check failed")
		return m, clearErrAfter()
	}
	// planUpdate (version.go) owns the verdict, shared with `tjek update` so
	// the two surfaces cannot disagree about the same binary; the sentences
	// stay here because they are translated and the CLI's aren't.
	switch action, hint := planUpdate(appVersion, msg.latest); action {
	case updateUpToDate:
		m.updateStatus = tr("Up to date (") + appVersion + ")"
		return m, nil
	case updateLocalBuild:
		m.updateStatus = tr("Latest release: ") + msg.latest + tr("; this is a local build (") + appVersion + ")"
		m.flashInfo(m.updateStatus)
		return m, clearErrAfter()
	case updateManaged:
		m.updateStatus = fmt.Sprintf(tr("Update available: %s. Run `%s`"), msg.latest, hint)
		m.flashInfo(m.updateStatus)
		return m, clearErrAfter()
	}
	// Newer release available — ask before pulling it.
	m.updateStatus = tr("Update available: ") + msg.latest
	m.mode = modeConfirmUpdate
	m.confirmMsg = msg.latest + tr(" is available. Update now? (y/n)")
	return m, nil
}

func (m model) handleSyncTick() (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{syncTick()}
	if m.autoSync {
		cmds = append(cmds, m.backgroundSync())
		// Mid-session enable: start the real-time listener if sync was just
		// turned on (it isn't running yet) and arm its reader once.
		if m.liveSync == nil {
			if ls := startLiveSync(m.syncCfg); ls != nil {
				m.liveSync = ls
				cmds = append(cmds, waitForSyncEvent(ls.C))
			}
		}
	}
	if p := m.probeServer(); p != nil {
		cmds = append(cmds, p)
	}
	return m, tea.Batch(cmds...)
}

// handleSyncEvent answers the server signalling a change: re-arm the
// listener and pull now.
func (m model) handleSyncEvent() (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.liveSync != nil {
		cmds = append(cmds, waitForSyncEvent(m.liveSync.C))
	}
	if m.autoSync {
		cmds = append(cmds, m.backgroundSync())
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// dayTickMsg arrives once a minute so the derived views roll over at
// midnight (overdue, due today, a start date reaching today) without waiting
// for a key. The wall clock is read on every tick, so a machine waking from
// sleep catches up on its next one.
type dayTickMsg struct{ at time.Time }

func dayTick() tea.Cmd {
	return tea.Tick(time.Minute, func(t time.Time) tea.Msg { return dayTickMsg{at: t} })
}

func (m model) handleDayTick(msg dayTickMsg) (tea.Model, tea.Cmd) {
	if !startOfDay(m.cache.builtAt).Equal(startOfDay(msg.at)) {
		m.markCacheDirty()
	}
	return m, dayTick()
}

func (m model) handleSaveTick() (tea.Model, tea.Cmd) {
	m.saveScheduled = false
	if !m.savePending {
		return m, nil
	}
	if m.savesInFlight > 0 {
		return m, nil // saveFinished schedules it
	}
	m.savePending = false
	// Drain only the dirty IDs and tombstones from the Store. The per-task
	// deep copies drainDirty makes are what keep this save goroutine safe
	// from the mutations the Update goroutine keeps making while it runs.
	c := m.Store.drainDirty()
	if c.empty() {
		return m, nil
	}
	m.beginSave()
	repo := m.repo
	if m.watcher != nil {
		// Record the timestamp BEFORE the save so a fast fs event firing
		// during the write is still inside the suppression window. The save
		// goroutine doesn't need to update this.
		m.watcher.recordSelfSave()
	}
	return m, tea.Batch(func() tea.Msg {
		if err := repo.SaveOnto(c.dirty, c.bases, c.tombstones); err != nil {
			return saveErrMsg{err}
		}
		return saveDoneMsg{saved: c.dirty, sent: c.sent}
	}, m.exportSoon())
}

// handleDBChanged answers an external writer (CLI, another process) touching
// the DB: reload now, or defer until the user exits a modal mode. The watcher
// channel listener is always re-armed.
func (m model) handleDBChanged() (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.watcher != nil {
		cmds = append(cmds, waitForDBChange(m.watcher.ch))
		if m.watcher.shouldReloadNow(time.Now(), m.mode) {
			cmds = append(cmds, m.reloadCmd())
		}
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

func (m model) handleReloaded(msg reloadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.flashError(fmt.Sprintf("External reload failed: %v", msg.err))
		return m, clearErrAfter()
	}
	if msg.epoch != m.saveEpoch {
		// A save started after this read did, which may have missed it.
		return m, m.reloadCmd()
	}
	// Atomic swap: rebuild the Store from the freshly-loaded task set,
	// invalidate caches, and follow the same task ID across the new ordering
	// so the cursor stays anchored where the user expected.
	//
	// The swap must not wipe what only exists in memory: the undo stack, and
	// any mutation still inside the save debounce (dirty tasks and pending
	// tombstones the snapshot predates). A dirty task takes its edits onto
	// the loaded version (rebase), which becomes its base; one with no base
	// stays as it is, to be saved whole. The change set is carried across so
	// the scheduled save still flushes it.
	// A reload the user cannot see is the common case, not the exception: the
	// watcher fires on our own WAL writes, on a sync that merged nothing, on a
	// checkpoint. Rebuilding the whole Store for those costs ~15ms at a couple
	// of thousand tasks — on the Update goroutine, so it lands as a stutter on
	// whatever key is pressed next. Compare a cheap fingerprint first and skip
	// the swap when the snapshot says what we already have. Pending local
	// changes make the snapshot stale by definition, so the guard only applies
	// when there are none.
	if len(m.dirtyIDs) == 0 && len(m.tombstones) == 0 && m.sameAsLoaded(msg.todos) {
		return m, nil
	}

	taskID := m.currentTaskID()
	undo := m.undoStack
	dirtyIDs := m.dirtyIDs
	tombstones := m.tombstones
	dirtyTasks := make(map[string]todo.Todo, len(dirtyIDs))
	for id := range dirtyIDs {
		if t := m.get(id); t != nil {
			dirtyTasks[id] = copyTodo(*t)
		}
	}
	oldBase := m.base
	m.Store = Store{timers: m.timers}
	m.Store.ensureTasks()
	m.undoStack = undo
	m.dirtyIDs = dirtyIDs
	m.tombstones = tombstones
	for i := range msg.todos {
		t := &msg.todos[i]
		if _, dead := tombstones[t.ID]; dead {
			continue // deleted locally, deletion not yet flushed — stays dead
		}
		d, dirty := dirtyTasks[t.ID]
		switch b := oldBase[t.ID]; {
		case !dirty:
			m.Store.add(*t)
			m.Store.setBase(t)
		case b != nil:
			m.Store.add(rebase(t, b, &d))
			m.Store.setBase(t)
		default:
			m.Store.add(d)
		}
	}
	// Dirty tasks the snapshot doesn't hold: created here and not yet
	// saved, or gone from the store since, which their base still says.
	for id, d := range dirtyTasks {
		if m.get(id) == nil {
			m.Store.add(d)
			if b := oldBase[id]; b != nil {
				m.Store.setBase(b)
			}
		}
	}
	m.markCacheDirty()
	m.refreshCaches()
	m.followTask(taskID)
	return m, m.exportSoon() // another process changed the store
}
