-- Migration 018: add `add_requests`, the request IDs `tjek add --request-id`
-- was given and the task each one created (requests.go), so the same add run
-- again (an agent retrying after a timeout that had in fact succeeded)
-- answers with that task rather than making a second.
--
-- The row is written before the task, keeping the task's ID, so a retry after
-- an add that stopped between the two creates the task under the same ID: no
-- foreign key, since the task may not exist yet. Like claims, a request is
-- this store's alone and never syncs; a retry comes from the same machine.
-- Times are Unix milliseconds, and rows older than a week are dropped.
CREATE TABLE add_requests (
	request_id TEXT PRIMARY KEY,
	task_id    TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
