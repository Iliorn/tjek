package app

import (
	"database/sql"
	"time"

	"github.com/Iliorn/tjek/todo"
)

// requests.go makes `tjek add --request-id` safe to run twice (migration 018).
// An agent whose add timed out cannot tell whether the task was made, and
// searching before adding again does not help when two of its attempts run at
// once. With a request ID, the second add finds the first one's task.

// requestMemory is how long a request ID is remembered: retries come within
// minutes, and the table should not grow forever.
const requestMemory = 7 * 24 * time.Hour

// reserveRequest records that request creates the task taskID, unless the
// request was made before, and returns the task the request stands for:
// taskID when this call recorded it, the earlier task's ID when not (reused).
// It is one statement, so two adds with the same ID at once agree on one task.
func reserveRequest(h *sql.DB, request, taskID string, now time.Time) (id string, reused bool, err error) {
	if _, err := h.Exec(`DELETE FROM add_requests WHERE created_at < ?`, now.Add(-requestMemory).UnixMilli()); err != nil {
		return "", false, err
	}
	res, err := h.Exec(`INSERT INTO add_requests (request_id, task_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT(request_id) DO NOTHING`, request, taskID, now.UnixMilli())
	if err != nil {
		return "", false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", false, err
	} else if n == 1 {
		return taskID, false, nil
	}
	err = h.QueryRow(`SELECT task_id FROM add_requests WHERE request_id = ?`, request).Scan(&id)
	return id, true, err
}

// findStoredTask is the task with id as stored, deleted or not; nil when there is
// none.
func findStoredTask(id string) (*todo.Todo, error) {
	all, err := loadTodosForSync(db)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, nil
}
