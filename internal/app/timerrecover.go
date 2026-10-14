package app

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/Iliorn/tjek/todo"
)

// timerrecover.go keeps an abandoned timer from accruing forever. A live TUI
// heartbeats last_seen on its running entry (see the timer tick); on load — CLI
// or TUI — any running entry whose last credible activity is older than the
// staleness threshold is auto-stopped and reported, so a session that died
// mid-work (e.g. an agent that ran out of context) can't leave a 30-hour entry
// to be found days later.
//
// Honesty note: nothing recorded when a silently-killed process actually
// stopped, so recovery stops the timer at its last *observed* activity
// (last_seen, or started_at if it never heartbeated — which lands near zero for
// a CLI timer that was never the TUI's). That bounds the damage and surfaces it
// immediately; it does not pretend to know the true duration.

// heartbeatRunningTimers stamps last_seen=now on every running entry of this
// device's (sc). The TUI calls this on a throttled tick so its in-progress
// timer stays "fresh" and a concurrent CLI invocation won't mistake a live
// timer for an abandoned one. Someone else's timer on a shared task is theirs
// to keep fresh: one stamped here would never look abandoned.
func heartbeatRunningTimers(h *sql.DB, now time.Time, sc timerScope) error {
	ids, err := runningEntriesOf(h, sc)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := h.Exec(`UPDATE task_time_entries SET last_seen=? WHERE id=?`, fmtTime(now), id); err != nil {
			return err
		}
	}
	return nil
}

// runningEntriesOf is the IDs of the running entries of this device's (sc).
func runningEntriesOf(h *sql.DB, sc timerScope) ([]string, error) {
	rows, err := h.Query(`SELECT te.id, te.author, t.project
		FROM task_time_entries te JOIN todos t ON t.id = te.task_id
		WHERE te.stopped_at = '' AND te.deleted_at = ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, author, project string
		if err := rows.Scan(&id, &author, &project); err != nil {
			return nil, err
		}
		if (todo.TimeEntry{Author: author}).StartedBy(sc.ownerIn(project)) {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

type recoveredTimer struct {
	Title   string
	Started time.Time
	Logged  time.Duration
}

// reconcileStaleTimers stops every running entry of this device's (sc) idle
// longer than threshold, fixing its stop time at the last observed activity,
// and returns what it recovered so the caller can tell the user. Someone
// else's timer on a shared task is left alone: it is fresh only as of the
// last time their device wrote the file, and it is theirs to stop.
func reconcileStaleTimers(h *sql.DB, now time.Time, threshold time.Duration, sc timerScope) ([]recoveredTimer, error) {
	rows, err := h.Query(`SELECT te.id, te.started_at, te.last_seen, t.title, te.author, t.project
		FROM task_time_entries te JOIN todos t ON t.id = te.task_id
		WHERE te.stopped_at = '' AND te.deleted_at = '' AND t.deleted = 0`)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id           string
		started, ref time.Time
		title        string
	}
	var stale []cand
	for rows.Next() {
		var id, startedAt, lastSeen, title, author, project string
		if err := rows.Scan(&id, &startedAt, &lastSeen, &title, &author, &project); err != nil {
			rows.Close()
			return nil, err
		}
		if !(todo.TimeEntry{Author: author}).StartedBy(sc.ownerIn(project)) {
			continue
		}
		started := parseTime(startedAt)
		ref := parseTime(lastSeen)
		if ref.IsZero() {
			ref = started // never heartbeated → fall back to start
		}
		if ref.IsZero() {
			continue // no usable timestamp; leave it alone
		}
		if now.Sub(ref) > threshold {
			stale = append(stale, cand{id: id, started: started, ref: ref, title: title})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var recovered []recoveredTimer
	for _, c := range stale {
		// modified_at must move too: the sync merge orders entry versions by it,
		// and without the bump this stop ties with another device's still-running
		// copy (both carry the start-time stamp) and can lose the hash tiebreak —
		// resurrecting the abandoned timer. Normal stops via todo.StopTimer bump
		// ModifiedAt for the same reason.
		if _, err := h.Exec(`UPDATE task_time_entries SET stopped_at=?, modified_at=? WHERE id=?`,
			fmtTime(c.ref), fmtTime(now), c.id); err != nil {
			return recovered, err
		}
		logged := c.ref.Sub(c.started)
		if logged < 0 {
			logged = 0
		}
		recovered = append(recovered, recoveredTimer{Title: c.title, Started: c.started, Logged: logged})
	}
	return recovered, nil
}

// reconcileStaleTimersCLI runs the recoverer for a CLI invocation and warns on
// stderr. runCLI calls it only for commands that write to the store.
func reconcileStaleTimersCLI() {
	if err := openStore(); err != nil {
		return
	}
	recovered, err := reconcileStaleTimers(db, time.Now(), idleThreshold, cliTimerScope())
	if err != nil {
		return
	}
	for _, r := range recovered {
		fmt.Fprintf(os.Stderr,
			"tjek: auto-stopped a timer left running on %q since %s (idle over %s); logged %s. Fix it in the task's detail view if that's wrong.\n",
			r.Title, r.Started.Local().Format("Jan 2 15:04"), shortDur(idleThreshold), shortDur(r.Logged))
	}
}

// shortDur renders a duration as a compact, human string (e.g. "4h", "30m",
// "under a minute").
func shortDur(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return "under a minute"
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%dh%dm", d/time.Hour, (d%time.Hour)/time.Minute)
}
