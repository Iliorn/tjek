package app

import (
	"database/sql"
	"strings"
	"time"

	"github.com/Iliorn/tjek/todo"
)

// claims.go keeps the reservations held on tasks (migration 017). Several
// agents working from one list each take a task with `tjek claim` before
// starting on it, so two of them never do the same work. A claim runs out on
// its own (Until), so an agent that stops without releasing frees its task by
// itself, and renewing is claiming again.
//
// The test of whether a task is free and the taking of it are one SQL
// statement (takeClaim), so two processes claiming at once cannot both win:
// SQLite runs one write at a time, and the second finds the first's claim.
// A claim lives in this store only, beside the task rather than on it, so it
// takes no part in sync, export or the history.

// claimMark is the glyph a claimed task carries in lists, the app's and the
// CLI's alike.
const claimMark = "⚑"

// claim is one task's reservation: who holds it, since when, and until when.
type claim struct {
	Holder string
	Since  time.Time
	Until  time.Time
}

// claimJSON is a claim as --json shows it, on the task it reserves.
type claimJSON struct {
	By    string    `json:"by"`
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

func (c claim) json() *claimJSON {
	return &claimJSON{By: c.Holder, Since: c.Since, Until: c.Until}
}

// claimFor is the claim that holds t at now: none for a task that is closed
// or deleted (its claim is let go when it closes, and one still on record
// says nothing), nor for a claim that has run out.
func claimFor(claims map[string]claim, t *todo.Todo, now time.Time) (claim, bool) {
	c, ok := claims[t.ID]
	if !ok || t.Status != todo.Pending || t.Deleted || !now.Before(c.Until) {
		return claim{}, false
	}
	return c, true
}

// loadClaims reads the claims that have not run out at now, by task ID.
func loadClaims(h querier, now time.Time) (map[string]claim, error) {
	rows, err := h.Query(`SELECT task_id, holder, claimed_at, expires_at FROM claims WHERE expires_at > ?`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]claim{}
	for rows.Next() {
		var id, holder string
		var since, until int64
		if err := rows.Scan(&id, &holder, &since, &until); err != nil {
			return nil, err
		}
		out[id] = claim{Holder: holder, Since: time.UnixMilli(since), Until: time.UnixMilli(until)}
	}
	return out, rows.Err()
}

// takeClaim reserves task id for holder until now+ttl, if no one else holds
// it: it is unclaimed, its claim has run out, or (renew) holder already has
// it, which then runs on from now with its start kept. It reports whether
// the claim was taken, and when it was not, the claim that stood in the way.
func takeClaim(h *sql.DB, id, holder string, ttl time.Duration, now time.Time, renew bool) (claim, bool, error) {
	at, until := now.UnixMilli(), now.Add(ttl).UnixMilli()
	// The WHERE on the upsert is the whole test: a row it rejects is left as
	// it was and counts no change. Every expression reads the row as it was
	// before the update, so claimed_at compares the old holder.
	res, err := h.Exec(`INSERT INTO claims (task_id, holder, claimed_at, expires_at) VALUES (?1, ?2, ?3, ?4)
		ON CONFLICT(task_id) DO UPDATE SET
			claimed_at = CASE WHEN claims.holder = excluded.holder AND claims.expires_at > ?3
				THEN claims.claimed_at ELSE excluded.claimed_at END,
			holder = excluded.holder,
			expires_at = excluded.expires_at
		WHERE claims.expires_at <= ?3 OR (?5 AND claims.holder = excluded.holder)`,
		id, holder, at, until, renew)
	if err != nil {
		return claim{}, false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return claim{}, false, err
	} else if n == 0 {
		held, err := claimOn(h, id)
		return held, false, err
	}
	c, err := claimOn(h, id)
	return c, true, err
}

// claimOn is the claim on record for task id, run out or not.
func claimOn(h *sql.DB, id string) (claim, error) {
	var holder string
	var since, until int64
	err := h.QueryRow(`SELECT holder, claimed_at, expires_at FROM claims WHERE task_id = ?`, id).
		Scan(&holder, &since, &until)
	if err == sql.ErrNoRows {
		return claim{}, nil
	}
	if err != nil {
		return claim{}, err
	}
	return claim{Holder: holder, Since: time.UnixMilli(since), Until: time.UnixMilli(until)}, nil
}

// releaseClaim gives task id's claim back: holder's own, or (force) anyone's.
// A claim that has run out is gone whoever asks. It reports whether a claim
// was released, and when one was not, the claim held by someone else that
// stood in the way (zero when there was none to release).
func releaseClaim(h *sql.DB, id, holder string, force bool, now time.Time) (bool, claim, error) {
	res, err := h.Exec(`DELETE FROM claims WHERE task_id = ? AND (? OR holder = ? OR expires_at <= ?)`,
		id, force, holder, now.UnixMilli())
	if err != nil {
		return false, claim{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return n > 0, claim{}, err
	}
	held, err := claimOn(h, id)
	return false, held, err
}

// dropClaims lets go of the claims on ids, inside a save's transaction: a task
// closed or deleted is no longer anyone's to work on.
func dropClaims(tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	_, err := tx.Exec(`DELETE FROM claims WHERE task_id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	return err
}

// renewClaims runs holder's live claims on ids on for defaultClaimTTL from
// now, inside a save's transaction: an agent changing its task (a comment, an
// edit, a time entry) is still on it, so it does not lose the task for not
// claiming it again. A claim taken for longer keeps its later end.
func renewClaims(tx *sql.Tx, ids []string, holder string, now time.Time) error {
	if len(ids) == 0 || holder == "" {
		return nil
	}
	args := []any{now.Add(defaultClaimTTL).UnixMilli(), holder, now.UnixMilli()}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := tx.Exec(`UPDATE claims SET expires_at = MAX(expires_at, ?) WHERE holder = ? AND expires_at > ?
		AND task_id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	return err
}

// claimReader is the part of a Repository that can read claims. It is not on
// the Repository port: a claim is a CLI-side reservation the app only shows,
// and the test fakes have none.
type claimReader interface {
	Claims(now time.Time) (map[string]claim, error)
}

func (r *sqliteRepo) Claims(now time.Time) (map[string]claim, error) {
	if err := openStore(); err != nil {
		return nil, err
	}
	return loadClaims(db, now)
}

// repoClaims reads repo's claims, or none when it cannot hold any.
func repoClaims(repo Repository, now time.Time) map[string]claim {
	cr, ok := repo.(claimReader)
	if !ok {
		return nil
	}
	claims, err := cr.Claims(now)
	if err != nil {
		return nil
	}
	return claims
}
