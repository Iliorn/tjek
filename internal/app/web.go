package app

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/tasksync"
	"github.com/Iliorn/tjek/todo"
)

// web.go is the page `tjek serve --web` serves beside the sync endpoint, for
// a phone: the ranked list, a line to add a task with the quick-add grammar,
// and a button to close one. It is plain HTML and forms, with no script, so
// it costs the phone one small page per action.
//
// It reads and writes the server's own store through the same code as the
// CLI on that host (parseQuickAdd, closeTasks, the stamped save), signs its
// history events todo.SourceWeb, and rings the SSE hub after a write so the
// connected devices pull it at once.
//
// Access is the sync token: typed once on a login form, then carried as a
// cookie derived from it (webSession), so changing the token signs every
// browser out. Logins are checked one at a time, and a wrong one holds the
// next back for a second.

//go:embed web.tmpl
var webTemplateSrc string

var webTemplate = template.Must(template.New("web").Parse(webTemplateSrc))

const (
	webSessionCookie = "tjek_session"
	// webSessionAge is the cookie's lifetime, the longest browsers keep one.
	webSessionAge = 400 * 24 * time.Hour
	// webFormLimit caps a form body: a token or one line of a task.
	webFormLimit = 16 << 10
)

type webUI struct {
	token   string
	session string
	hub     *tasksync.Hub
	// secure marks the cookie https-only, set when the server serves TLS.
	secure bool
	// loginDelay is how long a wrong token holds loginMu, which every login
	// takes before it compares, so guesses from every client together are
	// checked at one per loginDelay.
	loginDelay time.Duration
	loginMu    sync.Mutex
	// mu serializes the page's requests: each loads, edits and saves the
	// store, and applies the settings' language, which is a global.
	mu sync.Mutex
}

func newWebUI(token string, hub *tasksync.Hub, secure bool) *webUI {
	return &webUI{token: token, session: webSession(token), hub: hub, secure: secure, loginDelay: time.Second}
}

// webSession is the cookie value a token signs in with. A MAC of a fixed
// string rather than the token itself, so the cookie never holds the token.
func webSession(token string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("tjek web session"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// handler serves /v1/ through sync and everything else as the page. The page
// refuses cross-site form posts (http.CrossOriginProtection); the sync routes
// are left as they are, since their clients send a bearer token, not a cookie.
func (w *webUI) handler(sync http.Handler) http.Handler {
	page := http.NewServeMux()
	page.HandleFunc("GET /{$}", w.handleList)
	page.HandleFunc("POST /login", w.handleLogin)
	page.HandleFunc("POST /add", w.handleAdd)
	page.HandleFunc("POST /done", w.handleDone)
	mux := http.NewServeMux()
	mux.Handle("/v1/", sync)
	mux.Handle("/", http.NewCrossOriginProtection().Handler(page))
	return mux
}

func (w *webUI) signedIn(r *http.Request) bool {
	c, err := r.Cookie(webSessionCookie)
	return err == nil && hmac.Equal([]byte(c.Value), []byte(w.session))
}

func (w *webUI) handleLogin(rw http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(rw, r.Body, webFormLimit)
	got := r.PostFormValue("token")
	// The compare runs under the lock. Comparing first and only then waiting
	// would delay the answer to a wrong guess, not the guessing: of many
	// guesses sent at once, a right one would come straight back.
	w.loginMu.Lock()
	ok := subtle.ConstantTimeCompare([]byte(got), []byte(w.token)) == 1
	if !ok {
		time.Sleep(w.loginDelay)
	}
	w.loginMu.Unlock()
	if !ok {
		w.mu.Lock()
		defer w.mu.Unlock()
		page := w.newPage(webSettings())
		page.Login = true
		page.Error = tr("Wrong token. The server has it in sync.json, or in TJEK_SYNC_TOKEN.")
		w.render(rw, r, http.StatusUnauthorized, page)
		return
	}
	http.SetCookie(rw, &http.Cookie{
		Name:     webSessionCookie,
		Value:    w.session,
		Path:     "/",
		MaxAge:   int(webSessionAge / time.Second),
		HttpOnly: true,
		Secure:   w.secure,
		// Lax rather than Strict: a link to the page from a chat or a note
		// is a cross-site navigation, and should open the list, not the
		// login. Cross-site posts get no cookie under Lax either.
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(rw, r, "/", http.StatusSeeOther)
}

func (w *webUI) handleList(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	settings := webSettings()
	page := w.newPage(settings)
	if !w.signedIn(r) {
		page.Login = true
		w.render(rw, r, http.StatusOK, page)
		return
	}
	_, todos, err := webLoad(settings)
	if err != nil {
		page.Error = fmt.Sprintf(tr("Could not load the tasks: %v"), err)
		w.render(rw, r, http.StatusInternalServerError, page)
		return
	}
	q := r.URL.Query()
	page.fill(todos, settings, time.Now(), q.Get("added"))
	if t := findByID(todos, q.Get("added")); t != nil {
		page.Toast = fmt.Sprintf(tr("Added: %s"), t.Title)
		if kept := keptWords(t.Title, q.Get("kept")); kept != "" {
			page.Error = fmt.Sprintf(tr("Kept in the title, not understood: %s"), kept)
		}
	}
	if id := q.Get("done"); id != "" {
		if t := findByID(todos, id); t != nil {
			page.Toast = fmt.Sprintf(tr("Done: %s"), t.Title)
		}
	}
	w.render(rw, r, http.StatusOK, page)
}

func (w *webUI) handleAdd(rw http.ResponseWriter, r *http.Request) {
	if !w.signedIn(r) {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, webFormLimit)
	line := strings.TrimSpace(r.PostFormValue("line"))
	w.mu.Lock()
	defer w.mu.Unlock()
	settings := webSettings()
	repo, todos, err := webLoad(settings)
	if err != nil {
		page := w.newPage(settings)
		page.Error = fmt.Sprintf(tr("Could not load the tasks: %v"), err)
		w.render(rw, r, http.StatusInternalServerError, page)
		return
	}
	// A refusal shows the list again with the line still in the field, so
	// the typing is not lost to a typo.
	refuse := func(msg string) {
		page := w.newPage(settings)
		page.fill(todos, settings, time.Now(), "")
		page.Line, page.Error = line, msg
		w.render(rw, r, http.StatusUnprocessableEntity, page)
	}
	parsed := parseQuickAdd(line)
	if parsed.title == "" {
		refuse(tr("A task needs a title"))
		return
	}
	t := todo.New(parsed.title)
	parsed.applyTo(&t)
	for _, ref := range parsed.deps {
		dep, err := resolveDepRef(todoPtrs(todos), ref)
		if err != nil {
			refuse(err.Error())
			return
		}
		t.AddDependency(dep.ID)
	}
	if err := repo.Save([]*todo.Todo{&t}, nil); err != nil {
		refuse(fmt.Sprintf(tr("Could not save: %v"), err))
		return
	}
	saveLastAddedID(t.ID)
	w.hub.Broadcast()
	to := url.Values{"added": {t.ID}}
	if len(parsed.unparsed) > 0 {
		to.Set("kept", strings.Join(parsed.unparsed, " "))
	}
	http.Redirect(rw, r, "/?"+to.Encode(), http.StatusSeeOther)
}

func (w *webUI) handleDone(rw http.ResponseWriter, r *http.Request) {
	if !w.signedIn(r) {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, webFormLimit)
	id := r.PostFormValue("id")
	// "leave" is the answer to the confirm page: close the task and leave
	// its open subtasks as they are, which is what the app's y does.
	leave := r.PostFormValue("subtasks") == "leave"
	w.mu.Lock()
	defer w.mu.Unlock()
	settings := webSettings()
	repo, todos, err := webLoad(settings)
	if err != nil {
		page := w.newPage(settings)
		page.Error = fmt.Sprintf(tr("Could not load the tasks: %v"), err)
		w.render(rw, r, http.StatusInternalServerError, page)
		return
	}
	t := findByID(todos, id)
	if t == nil || t.Status != todo.Pending {
		// Closed on another device since the page loaded, or a stale tab:
		// the list it lands on shows how things are.
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	children, get := sliceTaskLookups(todos)
	if pending := pendingDescendants(children, get, t.ID); len(pending) > 0 && !settings.AutoCloseSubtasks && !leave {
		page := w.newPage(settings)
		page.Confirm = &webConfirm{
			ID:       t.ID,
			Question: fmt.Sprintf(tr("Close '%s' with %d open subtask(s)?"), t.Title, len(pending)),
		}
		w.render(rw, r, http.StatusOK, page)
		return
	}
	// The save rebases onto the stored rows, so a sync that lands between
	// this load and the save keeps its edits to the same tasks.
	bases := make(map[string]*todo.Todo)
	for _, cid := range descendantIDsFrom(children, t.ID) {
		if c := get(cid); c != nil {
			bases[cid] = baseCopy(c)
		}
	}
	leaveOpen := func(*todo.Todo, int) (bool, bool) { return false, true }
	out := closeTasks(repo.ranker(), todos, []*todo.Todo{t}, "", cliTimerScope(), settings.AutoCloseSubtasks, leaveOpen)
	if err := repo.SaveOnto(out.saveSet(), bases, nil); err != nil {
		page := w.newPage(settings)
		page.fill(todos, settings, time.Now(), "")
		page.Error = fmt.Sprintf(tr("Could not save: %v"), err)
		w.render(rw, r, http.StatusInternalServerError, page)
		return
	}
	w.hub.Broadcast()
	http.Redirect(rw, r, "/?"+url.Values{"done": {t.ID}}.Encode(), http.StatusSeeOther)
}

// webSettings loads settings.json for one request and applies its language,
// so a change made in the app on the server shows on the next page load.
func webSettings() appSettings {
	settings, err := loadSettings()
	if err != nil {
		log.Printf("tjek serve: web: %v (using defaults)", err)
	}
	applyLang(settings.Language)
	return settings
}

// webLoad is loadForCLI for the page: the repository signs its saves as the
// web, and the ranker carries the momentum the app ranks with.
func webLoad(settings appSettings) (*sqliteRepo, []todo.Todo, error) {
	repo := newSQLiteRepo()
	repo.by = editor{name: authorName(settings), source: todo.SourceWeb}
	repo.SetRanker(rank.Ranker{Biases: biasesFromSettings(settings)})
	todos, err := repo.Load()
	if err == nil {
		repo.SetRanker(repo.ranker().Refreshed(time.Now(), todoPtrs(todos)))
	}
	return repo, todos, err
}

// keptWords is the add redirect's list of words the grammar did not take,
// when every one of them is in the new task's title, where they were kept;
// otherwise "". The list comes back in the URL, and a link that put its own
// words there would otherwise have the page say them.
func keptWords(title, kept string) string {
	words := strings.Fields(kept)
	if len(words) == 0 {
		return ""
	}
	inTitle := strings.Fields(title)
	for _, w := range words {
		if !slices.Contains(inTitle, w) {
			return ""
		}
	}
	return strings.Join(words, " ")
}

// findByID returns the task with exactly this ID, pointing into todos. The
// forms carry full IDs, so no prefix or title matching is wanted here.
func findByID(todos []todo.Todo, id string) *todo.Todo {
	if id == "" {
		return nil
	}
	for i := range todos {
		if todos[i].ID == id {
			return &todos[i]
		}
	}
	return nil
}

// ── The page ─────────────────────────────────────────────────────────────────

type webPage struct {
	Lang  string
	Theme template.CSS
	L     webLabels

	Login   bool
	Confirm *webConfirm

	Count      string
	Rows       []webRow
	ClosedHead string
	Closed     []string

	// Line refills the add field after a refusal; Toast confirms the last
	// action; Error says what went wrong.
	Line  string
	Toast string
	Error string
}

// webLabels is every fixed string the page shows, translated in Go so the
// translation test finds them (the template holds no English of its own).
type webLabels struct {
	Placeholder, NewTask, Add, MarkDone string
	SignIn, TokenLabel, LogIn           string
	Empty, CloseIt, Cancel              string
}

type webConfirm struct {
	ID, Question string
}

type webRow struct {
	ID, Title, Score string
	Due, DueClass    string
	Priority         string
	Subtasks         string
	Project          string
	Tags             []string
	Blocked          string
	Overdue, Fresh   bool
}

func (w *webUI) newPage(settings appSettings) webPage {
	lang := settings.Language
	if lang == "" {
		lang = string(langEN)
	}
	return webPage{
		Lang:  lang,
		Theme: webThemeCSS(themeByName(settings.Theme)),
		L: webLabels{
			Placeholder: tr("Buy milk #shopping due:friday"),
			NewTask:     tr("New task"),
			Add:         tr("Add"),
			MarkDone:    tr("Mark done: %s"),
			SignIn:      tr("Enter the server's sync token. This browser remembers it afterwards."),
			TokenLabel:  tr("Sync token"),
			LogIn:       tr("Log in"),
			Empty:       tr("Nothing open. Add a task above."),
			CloseIt:     tr("Close it"),
			Cancel:      tr("Cancel"),
		},
	}
}

// fill puts the list on the page: the open top-level tasks in the order and
// with the scores the Tasks tab shows (no search, no context: those belong to
// the machine they were set on), then what closed today. fresh is a task to
// highlight, the one just added.
func (p *webPage) fill(todos []todo.Todo, settings appSettings, now time.Time, fresh string) {
	all := todoPtrs(todos)
	rk := rank.Ranker{Biases: biasesFromSettings(settings)}.Refreshed(now, all)
	lifts := rank.Lifts(all, rk.Score)
	rk.Max = rank.MaxRanked(all, lifts, rk.Score, now)
	var vis listVisibility
	if !settings.ShowWaiting {
		vis.waiting, _ = waitingSet(all, now)
	}
	active, done := selectActiveDoneRanked(all, lifts, now, rk.ScoreAt(now), "", filterEnv{}, false, taskSortSequence, historySortCompleted, vis)
	blocked, _ := rank.DependencySets(all)
	type progress struct{ done, total int }
	subs := make(map[string]progress)
	for _, t := range all {
		if t.ParentID != "" && !t.Deleted {
			s := subs[t.ParentID]
			s.total++
			if t.Status == todo.Done {
				s.done++
			}
			subs[t.ParentID] = s
		}
	}

	p.Count = trCount("%d open", len(active), len(active))
	p.Rows = make([]webRow, 0, len(active))
	for i := range active {
		t := &active[i]
		row := webRow{ID: t.ID, Title: t.Title, Project: t.Project, Tags: t.Tags, Fresh: t.ID == fresh}
		if rank.StartsLater(t, now) {
			row.Score = startsCell(t.StartDate, now)
		} else {
			row.Score = rk.FormatPercent(rank.ScoreOf(t, lifts, rk.Score))
		}
		if !t.DueDate.IsZero() {
			row.Due = formatDueShort(t.DueDate, now)
			switch days := calendarDays(now, t.DueDate); {
			case days < 0:
				row.DueClass, row.Overdue = "late", true
			case days <= 1:
				row.DueClass = "soon"
			}
		}
		switch t.Priority {
		case todo.PriorityHigh:
			row.Priority = "↑ " + tr(t.Priority.String())
		case todo.PriorityLow:
			row.Priority = "↓ " + tr(t.Priority.String())
		}
		if s := subs[t.ID]; s.total > 0 {
			row.Subtasks = fmt.Sprintf("%d/%d", s.done, s.total)
		}
		if blocked[t.ID] {
			row.Blocked = tr("blocked")
		}
		p.Rows = append(p.Rows, row)
	}
	today := startOfDay(now)
	for i := range done {
		if startOfDay(done[i].CompletedAt).Equal(today) {
			p.Closed = append(p.Closed, done[i].Title)
		}
	}
	if len(p.Closed) > 0 {
		p.ClosedHead = strings.TrimSpace(fmt.Sprintf(tr("  Closed today (%d)"), len(p.Closed)))
	}
}

// webThemeCSS is the theme's palette as the page's CSS custom properties.
// The values are the theme table's own hex constants, never user input, which
// is what makes handing them to the template as template.CSS safe.
func webThemeCSS(t theme) template.CSS {
	vars := []struct {
		name string
		c    string
	}{
		{"bg", string(t.bg)},
		{"surface", string(t.sel)},
		{"line", string(mixHex(t.bg, t.dim, 0.7))},
		{"fg", string(t.fg)},
		{"muted", string(t.muted)},
		{"accent", string(t.accent)},
		{"purple", string(t.purple)},
		{"blue", string(t.blue)},
		{"teal", string(t.teal)},
		{"green", string(t.green)},
		{"yellow", string(t.yellow)},
		{"red", string(t.red)},
	}
	var b strings.Builder
	for _, v := range vars {
		fmt.Fprintf(&b, "--%s:%s;", v.name, v.c)
	}
	return template.CSS(b.String())
}

func (w *webUI) render(rw http.ResponseWriter, r *http.Request, status int, page webPage) {
	var buf bytes.Buffer
	if err := webTemplate.Execute(&buf, page); err != nil {
		log.Printf("tjek serve: web: render: %v", err)
		http.Error(rw, "render failed", http.StatusInternalServerError)
		return
	}
	h := rw.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// The page has no script, loads nothing from elsewhere and posts only to
	// itself; the policy says so, which leaves an injected tag nothing to do.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	if w.secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	h.Set("Vary", "Accept-Encoding")
	// The rows repeat the same markup, so a long list shrinks some twentyfold
	// (160 kB to 7 kB for 300 tasks): on a phone's connection that is most of
	// the load.
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		rw.WriteHeader(status)
		z := gzip.NewWriter(rw)
		_, _ = z.Write(buf.Bytes())
		_ = z.Close()
		return
	}
	rw.WriteHeader(status)
	_, _ = rw.Write(buf.Bytes())
}
