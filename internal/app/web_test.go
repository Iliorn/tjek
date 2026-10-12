package app

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Iliorn/tjek/tasksync"
	"github.com/Iliorn/tjek/todo"
)

// webTest is the page wired as `tjek serve --web` wires it, over a store of
// the test's own.
type webTest struct {
	t       testing.TB
	ui      *webUI
	handler http.Handler
	hub     *tasksync.Hub
}

func newWebTest(t *testing.T) *webTest {
	t.Helper()
	setTestHome(t, t.TempDir())
	testStore(t)
	return wireWebTest(t)
}

func wireWebTest(t testing.TB) *webTest {
	srv := newAppSyncServer("tok")
	ui := newWebUI("tok", srv.Hub, true)
	ui.loginDelay = 0
	return &webTest{t: t, ui: ui, handler: ui.handler(srv.Handler()), hub: srv.Hub}
}

// do sends a request; signedIn adds the session cookie a login sets.
func (w *webTest) do(method, target string, form url.Values, signedIn bool) *httptest.ResponseRecorder {
	w.t.Helper()
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if signedIn {
		r.AddCookie(&http.Cookie{Name: webSessionCookie, Value: webSession("tok")})
	}
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, r)
	return rec
}

func (w *webTest) seed(tasks ...todo.Todo) {
	w.t.Helper()
	ptrs := make([]*todo.Todo, len(tasks))
	for i := range tasks {
		ptrs[i] = &tasks[i]
	}
	if err := newSQLiteRepo().Save(ptrs, nil); err != nil {
		w.t.Fatalf("seed: %v", err)
	}
}

func (w *webTest) load() []todo.Todo {
	w.t.Helper()
	todos, err := newSQLiteRepo().Load()
	if err != nil {
		w.t.Fatalf("load: %v", err)
	}
	return todos
}

func TestWebAsksForTheTokenBeforeShowingAnything(t *testing.T) {
	w := newWebTest(t)
	w.seed(todo.New("Renew passport"))

	rec := w.do(http.MethodGet, "/", nil, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="token"`) {
		t.Fatalf("signed out: want the login form, got %d:\n%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "Renew passport") {
		t.Error("the login page shows a task")
	}

	rec = w.do(http.MethodPost, "/add", url.Values{"line": {"Sneaky"}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("signed-out add: want a redirect to the login, got %d", rec.Code)
	}
	if got := len(w.load()); got != 1 {
		t.Errorf("signed-out add saved a task: %d tasks", got)
	}
}

func TestWebLoginSetsACookieThatIsNotTheToken(t *testing.T) {
	w := newWebTest(t)

	rec := w.do(http.MethodPost, "/login", url.Values{"token": {"wrong"}}, false)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: want 401, got %d", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a wrong token set a cookie")
	}

	rec = w.do(http.MethodPost, "/login", url.Values{"token": {"tok"}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("right token: want 303, got %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("want one cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Value == "tok" || c.Value != webSession("tok") {
		t.Errorf("cookie value %q: want the session MAC, not the token", c.Value)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie flags: HttpOnly=%v Secure=%v SameSite=%v", c.HttpOnly, c.Secure, c.SameSite)
	}
	if webSession("other") == c.Value {
		t.Error("a different token signs in with the same cookie")
	}
}

func TestWebAddUnderstandsTheQuickAddLine(t *testing.T) {
	w := newWebTest(t)
	nudges := w.hub.Subscribe()
	defer w.hub.Unsubscribe(nudges)

	rec := w.do(http.MethodPost, "/add", url.Values{"line": {"Buy milk #shopping @home due:tomorrow p:high"}}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add: want 303, got %d:\n%s", rec.Code, rec.Body)
	}
	todos := w.load()
	if len(todos) != 1 {
		t.Fatalf("want one task, got %d", len(todos))
	}
	got := todos[0]
	tomorrow := startOfDay(time.Now()).AddDate(0, 0, 1)
	if got.Title != "Buy milk" || got.Project != "home" || got.Priority != todo.PriorityHigh ||
		len(got.Tags) != 1 || got.Tags[0] != "shopping" || !startOfDay(got.DueDate).Equal(tomorrow) {
		t.Errorf("parsed task: %+v", got)
	}
	if loc := rec.Header().Get("Location"); loc != "/?added="+got.ID {
		t.Errorf("redirect %q: want the list naming the new task", loc)
	}
	if len(got.History) == 0 || got.History[0].Source != todo.SourceWeb {
		t.Errorf("history: want a created event from the web, got %+v", got.History)
	}
	select {
	case <-nudges:
	default:
		t.Error("the add did not nudge the connected devices")
	}

	page := w.do(http.MethodGet, rec.Header().Get("Location"), nil, true).Body.String()
	if !strings.Contains(page, "Added: Buy milk") || !strings.Contains(page, `class="task fresh"`) {
		t.Errorf("the list after an add does not confirm it:\n%s", page)
	}
}

func TestWebAddKeepsTheLineWhenItHasNoTitle(t *testing.T) {
	w := newWebTest(t)
	rec := w.do(http.MethodPost, "/add", url.Values{"line": {"#errand"}}, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="#errand"`) || !strings.Contains(body, "A task needs a title") {
		t.Errorf("the refusal lost the line or its reason:\n%s", body)
	}
	if n := len(w.load()); n != 0 {
		t.Errorf("a line with no title saved %d tasks", n)
	}
}

func TestWebListsTasksInTheAppsOrder(t *testing.T) {
	w := newWebTest(t)
	someday := todo.New("Read a novel")
	overdue := todo.New("File the tax return")
	overdue.DueDate = startOfDay(time.Now()).AddDate(0, 0, -2)
	closed := todo.New("Call the dentist")
	closed.Toggle()
	w.seed(someday, overdue, closed)

	body := w.do(http.MethodGet, "/", nil, true).Body.String()
	first, second := strings.Index(body, "File the tax return"), strings.Index(body, "Read a novel")
	if first < 0 || second < 0 || first > second {
		t.Errorf("want the overdue task above the undated one:\n%s", body)
	}
	if !strings.Contains(body, `class="task overdue"`) || !strings.Contains(body, ">-2d<") {
		t.Errorf("the overdue task is not marked as the app marks it:\n%s", body)
	}
	if !strings.Contains(body, "Closed today (1)") || !strings.Contains(body, "Call the dentist") {
		t.Errorf("today's closed task is missing:\n%s", body)
	}
	if !strings.Contains(body, "2 open") {
		t.Errorf("want the open count:\n%s", body)
	}
}

func TestWebDoneClosesATaskAndSpawnsTheNextRepeat(t *testing.T) {
	w := newWebTest(t)
	weekly := todo.New("Water the plants")
	weekly.Recurrence = "weekly"
	weekly.DueDate = startOfDay(time.Now())
	w.seed(weekly)

	rec := w.do(http.MethodPost, "/done", url.Values{"id": {weekly.ID}}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("done: want 303, got %d:\n%s", rec.Code, rec.Body)
	}
	todos := w.load()
	var closed, next int
	for _, x := range todos {
		switch {
		case x.ID == weekly.ID && x.Status == todo.Done:
			closed++
		case x.ID != weekly.ID && x.Title == "Water the plants" && x.Status == todo.Pending:
			next++
		}
	}
	if closed != 1 || next != 1 {
		t.Errorf("want the task closed and its next instance open; closed=%d next=%d", closed, next)
	}
	page := w.do(http.MethodGet, rec.Header().Get("Location"), nil, true).Body.String()
	if !strings.Contains(page, "Done: Water the plants") {
		t.Errorf("the list after a close does not confirm it:\n%s", page)
	}
}

func TestWebDoneAsksBeforeLeavingSubtasksOpen(t *testing.T) {
	w := newWebTest(t)
	parent := todo.New("Quarterly report")
	child := todo.New("Collect the numbers")
	child.ParentID = parent.ID
	w.seed(parent, child)

	rec := w.do(http.MethodPost, "/done", url.Values{"id": {parent.ID}}, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "with 1 open subtask(s)?") {
		t.Fatalf("want the confirm page, got %d:\n%s", rec.Code, rec.Body)
	}
	if got := findByID(w.load(), parent.ID); got.Status != todo.Pending {
		t.Fatal("asking closed the task")
	}

	rec = w.do(http.MethodPost, "/done", url.Values{"id": {parent.ID}, "subtasks": {"leave"}}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirmed close: want 303, got %d", rec.Code)
	}
	todos := w.load()
	if findByID(todos, parent.ID).Status != todo.Done || findByID(todos, child.ID).Status != todo.Pending {
		t.Error("want the parent closed and its subtask left open, as the app's y does")
	}
}

func TestWebRefusesCrossSitePosts(t *testing.T) {
	w := newWebTest(t)
	r := httptest.NewRequest(http.MethodPost, "/add", strings.NewReader("line=Planted"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(&http.Cookie{Name: webSessionCookie, Value: webSession("tok")})
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site post: want 403, got %d", rec.Code)
	}
	if n := len(w.load()); n != 0 {
		t.Errorf("a cross-site post saved %d tasks", n)
	}
}

func TestWebLeavesTheSyncRoutesToSync(t *testing.T) {
	w := newWebTest(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(`{"tasks":[]}`))
	r.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Errorf("sync through the web handler: want 200, got %d: %s", rec.Code, rec.Body)
	}
}

func TestWebEscapesWhatTasksSay(t *testing.T) {
	w := newWebTest(t)
	w.seed(todo.New(`<script>alert(1)</script>`))
	body := w.do(http.MethodGet, "/", nil, true).Body.String()
	if strings.Contains(body, "<script>") {
		t.Errorf("a title reached the page unescaped:\n%s", body)
	}
}

func TestWebFollowsTheThemeAndLanguage(t *testing.T) {
	w := newWebTest(t)
	t.Cleanup(func() { applyLang(string(langEN)) })
	if err := saveSettings(appSettings{Theme: "Nord", Language: string(langDA)}); err != nil {
		t.Fatal(err)
	}
	body := w.do(http.MethodGet, "/", nil, true).Body.String()
	if !strings.Contains(body, "--bg:#2e3440;") {
		t.Errorf("want Nord's background:\n%s", body)
	}
	if !strings.Contains(body, `lang="da"`) || !strings.Contains(body, ">Tilføj<") {
		t.Errorf("want the page in Danish:\n%s", body)
	}
}

func TestWebAddReachesTheSyncClients(t *testing.T) {
	w := newWebTest(t)
	w.do(http.MethodPost, "/add", url.Values{"line": {"Book the ferry"}}, true)
	r := httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(`{"tasks":[]}`))
	r.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"title":"Book the ferry"`) {
		t.Errorf("a client's sync does not bring the task added on the page: %d %s", rec.Code, rec.Body)
	}
}

// BenchmarkWebList is one load of the page over 300 open tasks: the store
// read, the ranking and the render.
func BenchmarkWebList(b *testing.B) {
	b.Setenv("TJEK_HOME", b.TempDir())
	closeStoreSingleton()
	b.Cleanup(closeStoreSingleton)
	if err := openStore(); err != nil {
		b.Fatal(err)
	}
	w := wireWebTest(b)
	tasks := benchTodos(300)
	for i := range tasks {
		tasks[i].Status = todo.Pending
	}
	w.seed(tasks...)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if rec := w.do(http.MethodGet, "/", nil, true); rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

func TestWebCompressesThePageForBrowsersThatAsk(t *testing.T) {
	w := newWebTest(t)
	w.seed(todo.New("Renew passport"))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Encoding", "gzip, deflate, br")
	r.AddCookie(&http.Cookie{Name: webSessionCookie, Value: webSession("tok")})
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, r)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("want a gzipped page, got Content-Encoding %q", rec.Header().Get("Content-Encoding"))
	}
	z, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(z)
	if err != nil || !strings.Contains(string(body), "Renew passport") {
		t.Errorf("the gzipped page does not hold the list (err %v)", err)
	}
}

func TestWebChecksLoginsOneAtATime(t *testing.T) {
	w := newWebTest(t)
	w.ui.loginDelay = 300 * time.Millisecond
	go w.do(http.MethodPost, "/login", url.Values{"token": {"wrong"}}, false)
	// Wait until the wrong guess holds the lock, so the right one is sent
	// while it is being held back.
	for deadline := time.Now().Add(5 * time.Second); w.ui.loginMu.TryLock(); {
		w.ui.loginMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the wrong guess never took the login lock")
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	rec := w.do(http.MethodPost, "/login", url.Values{"token": {"tok"}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("right token: want 303, got %d", rec.Code)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Errorf("a right guess sent beside a wrong one came back in %v; it should wait its turn", waited)
	}
}

func TestWebSaysOnlyTheWordsItKeptInTheTitle(t *testing.T) {
	w := newWebTest(t)
	rec := w.do(http.MethodPost, "/add", url.Values{"line": {"Pay rent p:urgent"}}, true)
	page := w.do(http.MethodGet, rec.Header().Get("Location"), nil, true).Body.String()
	if !strings.Contains(page, "not understood: p:urgent") {
		t.Errorf("want the unparsed word named:\n%s", page)
	}

	id := w.load()[0].ID
	page = w.do(http.MethodGet, "/?"+url.Values{"added": {id}, "kept": {"Your session expired, sign in at evil.example"}}.Encode(), nil, true).Body.String()
	if strings.Contains(page, "evil.example") {
		t.Errorf("the page repeated words from the link that are not in the task:\n%s", page)
	}
}

func TestPlainHTTPReachable(t *testing.T) {
	for listen, want := range map[string]bool{
		"127.0.0.1:8765":     false,
		"100.101.102.103:80": false,
		"192.168.1.10:8765":  false,
		"0.0.0.0:8765":       true,
		":8765":              true,
		"[::]:8765":          true,
		"203.0.113.7:8765":   true,
	} {
		if got := plainHTTPReachable(listen); got != want {
			t.Errorf("plainHTTPReachable(%q) = %v, want %v", listen, got, want)
		}
	}
}
