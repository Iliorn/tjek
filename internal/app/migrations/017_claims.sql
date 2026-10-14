-- Migration 017: add `claims`, the reservations held on tasks (claims.go):
-- `tjek claim` takes one for a while, `tjek release` or closing the task
-- gives it back, so agents working from one list don't pick the same task.
--
-- A claim belongs to this store, not to the task: it is not a todos column,
-- so it never syncs, exports or enters the history. Times are Unix
-- milliseconds, so the claim's one-statement test of whether a task is free
-- compares integers. Deleting a task's row (tombstone pruning) drops its
-- claim.
CREATE TABLE claims (
	task_id    TEXT PRIMARY KEY REFERENCES todos(id) ON DELETE CASCADE,
	holder     TEXT NOT NULL,
	claimed_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
