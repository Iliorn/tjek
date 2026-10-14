package app

import (
	"database/sql"
	"errors"
	"maps"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Iliorn/tjek/todo"
	"github.com/charmbracelet/x/ansi"
)

// claimFixture is a store with the given tasks added through the CLI, and
// their IDs by title.
func claimFixture(t *testing.T, titles ...string) map[string]string {
	t.Helper()
	setTestHome(t, t.TempDir())
	for _, title := range titles {
		captureStdout(t, func() {
			if code := cliAdd([]string{title}); code != 0 {
				t.Fatalf("add %q: exit %d", title, code)
			}
		})
	}
	_, todos, err := loadForCLI()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, td := range todos {
		ids[td.Title] = td.ID
	}
	return ids
}

// cli runs one command as main does, and returns its exit code, stdout and
// stderr.
func cli(t *testing.T, readOnly bool, args ...string) (int, string, string) {
	t.Helper()
	var code int
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = runCLI(args, readOnly) })
	})
	return code, stdout, stderr
}

// freshProcess puts the store back to unopened, as the next command's process
// finds it.
func freshProcess() { closeStoreSingleton() }

func TestTakeClaim(t *testing.T) {
	h := openTestDB(t)
	task := todo.New("one task")
	saveTodos(t, h, []todo.Todo{task})
	now := time.Now().Truncate(time.Millisecond)

	c, took, err := takeClaim(h, task.ID, "agent-a", 30*time.Minute, now, true)
	if err != nil || !took || c.Holder != "agent-a" || !c.Until.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("a free task: took=%v claim=%+v err=%v", took, c, err)
	}
	c, took, _ = takeClaim(h, task.ID, "agent-b", 30*time.Minute, now.Add(time.Minute), true)
	if took || c.Holder != "agent-a" {
		t.Fatalf("another holder took a held task: took=%v claim=%+v", took, c)
	}
	// Renewing runs the claim on from now and keeps when it began.
	later := now.Add(20 * time.Minute)
	c, took, _ = takeClaim(h, task.ID, "agent-a", 30*time.Minute, later, true)
	if !took || !c.Since.Equal(now) || !c.Until.Equal(later.Add(30*time.Minute)) {
		t.Fatalf("renewal: took=%v claim=%+v, want since %v until %v", took, c, now, later.Add(30*time.Minute))
	}
	// Without renew (claim --next), a holder's own claim is not taken again.
	if _, took, _ = takeClaim(h, task.ID, "agent-a", time.Minute, later, false); took {
		t.Error("a claim the holder already has was taken again without renew")
	}
	// Run out, it is anyone's, and starts afresh.
	expired := later.Add(31 * time.Minute)
	c, took, _ = takeClaim(h, task.ID, "agent-b", 30*time.Minute, expired, true)
	if !took || c.Holder != "agent-b" || !c.Since.Equal(expired) {
		t.Fatalf("an expired claim: took=%v claim=%+v", took, c)
	}
	if live, _ := loadClaims(h, expired.Add(31*time.Minute)); len(live) != 0 {
		t.Errorf("loadClaims kept a claim past its end: %v", live)
	}
}

// The test and the taking are one statement, so processes racing for one
// task cannot both get it.
func TestClaimRaceHasOneWinner(t *testing.T) {
	ids := claimFixture(t, "Contested task")
	id := ids["Contested task"]
	freshProcess()
	const agents = 8
	handles := make([]*sql.DB, agents)
	for i := range handles {
		h, err := openStoreAt(dbPath())
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		handles[i] = h
	}
	var wg sync.WaitGroup
	wins := make(chan string, agents)
	start := make(chan struct{})
	for i, h := range handles {
		wg.Add(1)
		go func(i int, h *sql.DB) {
			defer wg.Done()
			<-start
			holder := "agent-" + string(rune('a'+i))
			if _, took, err := takeClaim(h, id, holder, time.Minute, time.Now(), true); err != nil {
				t.Errorf("%s: %v", holder, err)
			} else if took {
				wins <- holder
			}
		}(i, h)
	}
	close(start)
	wg.Wait()
	close(wins)
	var winners []string
	for w := range wins {
		winners = append(winners, w)
	}
	if len(winners) != 1 {
		t.Fatalf("%d agents won the same task: %v", len(winners), winners)
	}
}

func TestCLIClaimAndRelease(t *testing.T) {
	claimFixture(t, "Write the report")

	if code, out, _ := cli(t, false, "claim", "report", "--as", "agent-a"); code != 0 || !strings.Contains(out, "for agent-a until") {
		t.Fatalf("claim: exit %d, %q", code, out)
	}
	freshProcess()
	code, _, errOut := cli(t, false, "claim", "report", "--as", "agent-b")
	if code != exitClaimed || !strings.Contains(errOut, "claimed by agent-a") {
		t.Fatalf("a second agent's claim: exit %d, %q; want %d naming the holder", code, errOut, exitClaimed)
	}
	freshProcess()
	if code, _, errOut := cli(t, false, "release", "report", "--as", "agent-b"); code != exitClaimed || !strings.Contains(errOut, "-f releases it") {
		t.Fatalf("releasing another's claim: exit %d, %q", code, errOut)
	}
	freshProcess()
	if code, out, _ := cli(t, false, "release", "report", "--as", "agent-b", "-f"); code != 0 || !strings.Contains(out, "released") {
		t.Fatalf("release -f: exit %d, %q", code, out)
	}
	freshProcess()
	if code, _, errOut := cli(t, false, "release", "report", "--as", "agent-a"); code != 0 || !strings.Contains(errOut, "is not claimed") {
		t.Fatalf("releasing an unclaimed task: exit %d, %q", code, errOut)
	}

	// TJEK_AUTHOR names the holder when --as does not, as it signs edits.
	freshProcess()
	t.Setenv("TJEK_AUTHOR", "agent-c")
	if code, out, _ := cli(t, false, "claim", "report"); code != 0 || !strings.Contains(out, "for agent-c") {
		t.Fatalf("claim as TJEK_AUTHOR: exit %d, %q", code, out)
	}
	freshProcess()
	code, out, _ := cli(t, false, "show", "report")
	if code != 0 || !strings.Contains(out, "Claimed:  by agent-c until") {
		t.Errorf("show does not say who holds the task:\n%s", out)
	}
	freshProcess()
	if _, out, _ := cli(t, false, "list"); !strings.Contains(out, claimMark+" agent-c · Write the report") {
		t.Errorf("list does not mark the claimed task:\n%s", out)
	}
}

func TestCLIClaimNext(t *testing.T) {
	ids := claimFixture(t, "Ready one", "Ready two", "Blocked", "Waiting", "Timed", "Taken")
	freshProcess()
	captureStdout(t, func() {
		cliEdit([]string{ids["Blocked"], "--add-dep", ids["Ready one"]})
		cliEdit([]string{ids["Waiting"], "--start", "+5d"})
		cliEdit([]string{ids["Ready one"], "--p", "h"})
		cliStart([]string{ids["Timed"]})
		cliClaim([]string{ids["Taken"], "--as", "someone-else"})
	})

	freshProcess()
	code, out, _ := cli(t, false, "claim", "--next", "--as", "agent-a")
	if code != 0 || !strings.Contains(out, `"Ready one"`) {
		t.Fatalf("first --next: exit %d, %q; want the highest-ranked ready task", code, out)
	}
	// The same agent asking again gets a new task, not the one it holds.
	freshProcess()
	code, out, _ = cli(t, false, "claim", "--next", "--as", "agent-a")
	if code != 0 || !strings.Contains(out, `"Ready two"`) {
		t.Fatalf("second --next: exit %d, %q", code, out)
	}
	// Left: one blocked, one waiting, one with a timer running, one taken.
	freshProcess()
	code, _, errOut := cli(t, false, "claim", "--next", "--as", "agent-b")
	if code != exitClaimed || !strings.Contains(errOut, "no ready task is unclaimed") {
		t.Fatalf("--next with nothing free: exit %d, %q", code, errOut)
	}
	freshProcess()
	if _, out, _ := cli(t, false, "list", "--unclaimed"); strings.Contains(out, "Ready one") || !strings.Contains(out, "Blocked") {
		t.Errorf("list --unclaimed:\n%s", out)
	}
	freshProcess()
	if _, out, _ := cli(t, false, "top", "--unclaimed"); strings.Contains(out, "Ready") || strings.Contains(out, "Taken") {
		t.Errorf("top --unclaimed shows claimed tasks:\n%s", out)
	}
}

// A closed or deleted task is no one's to work on: the save lets its claim
// go, whatever made the change.
func TestClosingOrDeletingReleasesTheClaim(t *testing.T) {
	ids := claimFixture(t, "Finish me", "Delete me")
	freshProcess()
	captureStdout(t, func() {
		cliClaim([]string{ids["Finish me"], "--as", "agent-a"})
		cliClaim([]string{ids["Delete me"], "--as", "agent-a"})
		cliDone([]string{ids["Finish me"]})
		cliDelete([]string{ids["Delete me"]})
	})
	var n int
	if err := testStore(t).QueryRow(`SELECT count(*) FROM claims`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d claims left after closing and deleting their tasks (err %v)", n, err)
	}
	// Reopening the task does not bring the claim back.
	captureStdout(t, func() { cliReopen([]string{ids["Finish me"]}) })
	if claims, _ := loadClaims(testStore(t), time.Now()); len(claims) != 0 {
		t.Errorf("a reopened task is claimed again: %v", claims)
	}
}

func TestReadCommandCreatesNoStore(t *testing.T) {
	setTestHome(t, t.TempDir())
	code, out, errOut := cli(t, false, "list")
	if code != 1 || !strings.Contains(errOut, "no task store") {
		t.Fatalf("list without a store: exit %d, %q %q", code, out, errOut)
	}
	if _, err := os.Stat(dbPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list created a store: %v", err)
	}
}

// A lookup leaves a timer alone, however stale: stopping it is a write, and
// the next command that writes does it.
func TestReadCommandRecoversNoTimer(t *testing.T) {
	setTestHome(t, t.TempDir())
	task := taskWithTimer("left running", todo.TimeEntry{ID: "te1", StartedAt: time.Now().Add(-48 * time.Hour)})
	saveTodos(t, testStore(t), []todo.Todo{task})
	freshProcess()
	if code, _, errOut := cli(t, false, "show", "left running"); code != 0 || strings.Contains(errOut, "auto-stopped") {
		t.Fatalf("show: exit %d, %q", code, errOut)
	}
	freshProcess()
	got, _ := loadTodosFromDB(testStore(t))
	if !got[0].TimeEntries[0].IsRunning() {
		t.Fatal("a read command stopped a timer")
	}
	freshProcess()
	if _, _, errOut := cli(t, false, "claim", "left running", "--as", "x"); !strings.Contains(errOut, "auto-stopped") {
		t.Errorf("a writing command did not recover the stale timer: %q", errOut)
	}
}

func TestReadOnlyRun(t *testing.T) {
	claimFixture(t, "Existing task")
	freshProcess()
	if code, out, _ := cli(t, true, "list"); code != 0 || !strings.Contains(out, "Existing task") {
		t.Fatalf("read-only list: exit %d, %q", code, out)
	}
	for _, args := range [][]string{{"add", "New task"}, {"claim", "existing", "--as", "x"}, {"done", "existing"}, {"update", "-y"}} {
		freshProcess()
		code, _, errOut := cli(t, true, args...)
		if code != 1 || !strings.Contains(errOut, "refused in a read-only run") {
			t.Errorf("read-only %v: exit %d, %q", args, code, errOut)
		}
	}
	freshProcess()
	if _, todos, _ := loadForCLI(); len(todos) != 1 || todos[0].Status != todo.Pending {
		t.Errorf("a refused command changed the store: %+v", todos)
	}
}

// A store older than this build is migrated by a plain read, which needs it to
// read at all, and refused by a read-only one, which may not write.
func TestReadOnlyRefusesAStoreThatNeedsMigrating(t *testing.T) {
	claimFixture(t, "Old store task")
	h := testStore(t)
	if _, err := h.Exec(`DROP TABLE claims; DELETE FROM schema_version WHERE version = 17`); err != nil {
		t.Fatal(err)
	}
	freshProcess()
	code, _, errOut := cli(t, true, "list")
	if code != 1 || !strings.Contains(errOut, "needs migrating") {
		t.Fatalf("read-only list of an unmigrated store: exit %d, %q", code, errOut)
	}
	freshProcess()
	if v, _ := currentSchemaVersion(rawStore(t)); v != 16 {
		t.Fatalf("the refused read migrated the store to %d", v)
	}
	if code, out, _ := cli(t, false, "list"); code != 0 || !strings.Contains(out, "Old store task") {
		t.Fatalf("plain list of an unmigrated store: exit %d, %q", code, out)
	}
	freshProcess()
	if v, _ := currentSchemaVersion(rawStore(t)); v != 17 {
		t.Errorf("a plain read left the store at %d", v)
	}
}

// rawStore opens the store file as it is, without migrating it.
func rawStore(t *testing.T) *sql.DB {
	t.Helper()
	h, err := openStoreFileAt(dbPath(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func TestTakeReadOnly(t *testing.T) {
	t.Setenv("TJEK_READ_ONLY", "")
	for _, c := range []struct {
		args []string
		want string
		ro   bool
	}{
		{[]string{"list"}, "list", false},
		{[]string{"--read-only", "list", "--json"}, "list --json", true},
		{[]string{"show", "x", "--read-only"}, "show x", true},
		{[]string{"add", "--", "--read-only"}, "add -- --read-only", false},
	} {
		got, ro := takeReadOnly(c.args)
		if strings.Join(got, " ") != c.want || ro != c.ro {
			t.Errorf("takeReadOnly(%q) = %q, %v; want %q, %v", c.args, got, ro, c.want, c.ro)
		}
	}
	t.Setenv("TJEK_READ_ONLY", "1")
	if _, ro := takeReadOnly([]string{"list"}); !ro {
		t.Error("TJEK_READ_ONLY=1 does not make the run read-only")
	}
}

func TestWantsJSON(t *testing.T) {
	for args, want := range map[string]bool{
		"list --json":           true,
		"show x -json":          true,
		"stats --format=json":   true,
		"stats --format json":   true,
		"stats --format waybar": false,
		"list":                  false,
		"add -- --json":         false,
	} {
		if got := wantsJSON(strings.Fields(args)); got != want {
			t.Errorf("wantsJSON(%q) = %v, want %v", args, got, want)
		}
	}
}

// A failed --json command still says what went wrong on stderr, and adds the
// JSON object on stdout; one that succeeds prints only its own answer.
func TestJSONErrorsKeepStderr(t *testing.T) {
	claimFixture(t, "Some task")
	freshProcess()
	code, out, errOut := cli(t, false, "show", "nothing-like-this", "--json")
	if code != 2 || !strings.Contains(errOut, "no task matches") || !strings.Contains(out, `"exit_code": 2`) {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	freshProcess()
	if code, out, _ := cli(t, false, "show", "some task", "--json"); code != 0 || strings.Contains(out, `"error"`) {
		t.Fatalf("a successful --json command: exit %d, %s", code, out)
	}
}

func TestAppShowsClaims(t *testing.T) {
	task := todo.New("an agent's task")
	other := todo.New("a free task")
	m := modelWithTasks(t, task, other)
	m.frameTime = time.Now()

	// A claim taken elsewhere changes no task, so it arrives with a reload
	// whose tasks are the ones already shown.
	claims := map[string]claim{task.ID: {Holder: "codex-1", Since: m.frameTime, Until: m.frameTime.Add(20 * time.Minute)}}
	next, _ := m.handleReloaded(reloadedMsg{todos: []todo.Todo{task, other}, epoch: m.saveEpoch, claims: claims})
	m = next.(model)
	if !maps.Equal(m.claims, claims) {
		t.Fatalf("the reload's claims were not adopted: %v", m.claims)
	}

	view := ansi.Strip(m.View())
	if !strings.Contains(view, claimMark+" An agent's task") {
		t.Errorf("the claimed row carries no %s:\n%s", claimMark, view)
	}
	if strings.Contains(view, claimMark+" A free task") {
		t.Errorf("an unclaimed row carries the mark:\n%s", view)
	}
	m.pane = paneDetail
	m.detailTaskID = task.ID
	m.invalidateDetailCache()
	if detail := ansi.Strip(m.buildDetailContent()); !strings.Contains(detail, "codex-1 · 20m left") {
		t.Errorf("the detail pane does not say who holds the task:\n%s", detail)
	}

	// Run out, the claim is shown nowhere.
	m.claims = map[string]claim{task.ID: {Holder: "codex-1", Since: m.frameTime.Add(-time.Hour), Until: m.frameTime.Add(-time.Minute)}}
	m.invalidateDetailCache()
	if view := ansi.Strip(m.View()); strings.Contains(view, claimMark+" An agent's task") || strings.Contains(view, "codex-1") {
		t.Errorf("an expired claim is still shown:\n%s", view)
	}
}
