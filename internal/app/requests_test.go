package app

import (
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
)

// liveTitles is the titles of the tasks not deleted, in the store as it is.
func liveTitles(t *testing.T) []string {
	t.Helper()
	freshProcess()
	_, todos, err := loadForCLI()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, td := range todos {
		out = append(out, td.Title)
	}
	return out
}

// The add an agent runs again after a timeout answers with the task the
// first run made, in the same shape, and adds nothing.
func TestAddRequestIDAddsOnce(t *testing.T) {
	setTestHome(t, t.TempDir())
	code, first, _ := cli(t, false, "add", "Write the report", "--request-id", "req-1", "--json")
	if code != 0 {
		t.Fatalf("first add: exit %d", code)
	}
	freshProcess()
	code, second, errOut := cli(t, false, "add", "Write the report", "--request-id", "req-1", "--json")
	if code != 0 || !strings.Contains(errOut, `request "req-1" already added`) {
		t.Fatalf("second add: exit %d, %q", code, errOut)
	}
	var a, b todo.Todo
	if json.Unmarshal([]byte(first), &a) != nil || json.Unmarshal([]byte(second), &b) != nil || a.ID == "" || a.ID != b.ID {
		t.Fatalf("the two adds answered with different tasks:\n%s\n%s", first, second)
	}
	if got := liveTitles(t); len(got) != 1 {
		t.Fatalf("store holds %v; want one task", got)
	}
	// Another request ID is another task, even with the same title.
	freshProcess()
	if code, _, _ := cli(t, false, "add", "Write the report", "--request-id", "req-2"); code != 0 {
		t.Fatalf("add with a new request ID: exit %d", code)
	}
	if got := liveTitles(t); len(got) != 2 {
		t.Errorf("store holds %v; want two tasks", got)
	}
}

// An add that reserved its request ID and stopped before saving left the ID
// naming a task that does not exist; the retry makes it, under that ID.
func TestAddRequestIDAfterAnInterruptedAdd(t *testing.T) {
	setTestHome(t, t.TempDir())
	if _, _, err := reserveRequest(testStore(t), "req-1", "11111111-1111-4111-8111-111111111111", time.Now()); err != nil {
		t.Fatal(err)
	}
	freshProcess()
	code, out, _ := cli(t, false, "add", "Interrupted", "--request-id", "req-1", "--quiet-id")
	if code != 0 || strings.TrimSpace(out) != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("retry: exit %d, %q; want the task made under the reserved ID", code, out)
	}
}

// A task the request made and someone deleted since is still its answer: the
// add happened, and doing it again would undo the deletion behind their back.
func TestAddRequestIDOfADeletedTask(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, id, _ := cli(t, false, "add", "Short-lived", "--request-id", "req-1", "--quiet-id")
	freshProcess()
	captureStdout(t, func() { cliDelete([]string{strings.TrimSpace(id)}) })
	freshProcess()
	code, _, errOut := cli(t, false, "add", "Short-lived", "--request-id", "req-1")
	if code != 0 || !strings.Contains(errOut, "has since been deleted") {
		t.Fatalf("retry after a delete: exit %d, %q", code, errOut)
	}
	if got := liveTitles(t); len(got) != 0 {
		t.Errorf("the retry added %v", got)
	}
}

func TestAddRequestIDRefusesBatch(t *testing.T) {
	setTestHome(t, t.TempDir())
	if code, _, errOut := cli(t, false, "add", "-", "--request-id", "req-1"); code != 2 || !strings.Contains(errOut, "names one task") {
		t.Fatalf("batch with a request ID: exit %d, %q", code, errOut)
	}
}

// Adds with one request ID at once agree on one task, whichever runs first.
func TestReserveRequestRace(t *testing.T) {
	setTestHome(t, t.TempDir())
	testStore(t)
	freshProcess()
	const adds = 8
	var wg sync.WaitGroup
	ids := make(chan string, adds)
	start := make(chan struct{})
	for i := 0; i < adds; i++ {
		h, err := openStoreAt(dbPath())
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		wg.Add(1)
		go func(i int, h *sql.DB) {
			defer wg.Done()
			<-start
			id, _, err := reserveRequest(h, "req-1", todo.New("x").ID, time.Now())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}(i, h)
	}
	close(start)
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("%d adds with one request ID were given %d tasks", adds, len(seen))
	}
}

func TestReserveRequestForgetsAfterAWeek(t *testing.T) {
	h := openTestDB(t)
	now := time.Now()
	if _, reused, _ := reserveRequest(h, "req-1", "a", now); reused {
		t.Fatal("a new request was reported as seen")
	}
	if id, reused, _ := reserveRequest(h, "req-1", "b", now.Add(time.Hour)); !reused || id != "a" {
		t.Fatalf("within the week: %q, %v", id, reused)
	}
	if id, reused, _ := reserveRequest(h, "req-1", "c", now.Add(requestMemory+time.Hour)); reused || id != "c" {
		t.Errorf("after the week: %q, %v; want the ID free again", id, reused)
	}
}
