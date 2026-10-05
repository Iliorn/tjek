-- Migration 015: add `recur_from`, the anchor of a recurring task's series
-- (todo.Todo.RecurFrom): the date its instances are counted from, so moving
-- one instance's due date leaves the series where it was, and a monthly
-- series on the 31st comes back to the 31st after February.
--
-- DEFAULT '' = no anchor yet: the series counts from its due date as it
-- stands, which is what every series did before this column, so existing
-- rows need no backfill.
ALTER TABLE todos ADD COLUMN recur_from TEXT NOT NULL DEFAULT '';
