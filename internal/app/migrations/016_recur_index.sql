-- Migration 016: add `recur_index`, which instance of its series a recurring
-- task is (todo.Todo.RecurIndex), counted from the anchor (`recur_from`) as
-- 0. The next instance comes after this place in the series, so one moved
-- earlier than its place is not followed by that place again.
--
-- DEFAULT 0 = the anchor's place, or a place not kept: the series goes on
-- from the instance's due date, which is what every series did before this
-- column, so existing rows need no backfill.
ALTER TABLE todos ADD COLUMN recur_index INTEGER NOT NULL DEFAULT 0;
