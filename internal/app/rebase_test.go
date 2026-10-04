package app

import (
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Iliorn/tjek/hlc"
	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/todo"
)

// ── The fold ─────────────────────────────────────────────────────────────────

// An edit here and one elsewhere to different fields both stay; to the same
// field, the one here wins, as the later.
func TestRebaseKeepsEditsElsewhereToOtherFields(t *testing.T) {
	base := todo.New("Book the ferry")
	stored, mine := copyTodo(base), copyTodo(base)
	stored.Title, stored.Notes = "Book the 9:00 ferry", "cabin 12"
	mine.Priority, mine.Notes = todo.PriorityHigh, "deck seats"

	got := rebase(&stored, &base, &mine)
	if got.Title != "Book the 9:00 ferry" || got.Priority != todo.PriorityHigh || got.Notes != "deck seats" {
		t.Errorf("rebased to title %q, priority %v, notes %q", got.Title, got.Priority, got.Notes)
	}
}

func TestRebaseFoldsTagsByMember(t *testing.T) {
	base := todo.New("Pack")
	base.Tags = []string{"trip", "home", "urgent"}
	stored, mine := copyTodo(base), copyTodo(base)
	stored.Tags = []string{"trip", "home", "kids"}  // urgent removed, kids added elsewhere
	mine.Tags = []string{"trip", "urgent", "beach"} // home removed, beach added here

	got := rebase(&stored, &base, &mine)
	if want := []string{"trip", "kids", "beach"}; !slices.Equal(got.Tags, want) {
		t.Errorf("tags %v, want %v", got.Tags, want)
	}
}

func TestRebaseFoldsCommentsByRecord(t *testing.T) {
	at := s0
	c := func(id, text string) todo.Comment {
		return todo.Comment{ID: id, Text: text, CreatedAt: at, ModifiedAt: at}
	}
	base := todo.New("Plan")
	base.Comments = []todo.Comment{c("kept", "a"), c("edited", "b"), c("removed-here", "c"), c("removed-there", "d")}
	stored, mine := copyTodo(base), copyTodo(base)
	stored.Comments = []todo.Comment{c("kept", "a"), c("edited", "b"), c("removed-here", "c"), c("added-there", "e")}
	stored.Comments[0].Author = "Anna" // a save filled the author in; not an edit
	mine.Comments = []todo.Comment{c("kept", "a"), c("edited", "B"), c("removed-there", "d"), c("added-here", "f")}
	mine.Comments[1].ModifiedAt = at.Add(time.Minute)

	got := rebase(&stored, &base, &mine)
	texts := map[string]string{}
	for _, x := range got.Comments {
		texts[x.ID] = x.Text
	}
	want := map[string]string{"kept": "a", "edited": "B", "added-there": "e", "added-here": "f"}
	if fmt.Sprint(texts) != fmt.Sprint(want) {
		t.Errorf("comments %v, want %v", texts, want)
	}
	if got.Comments[0].Author != "Anna" {
		t.Errorf("the untouched comment lost its stored author: %+v", got.Comments[0])
	}
}

// ── Through the store ────────────────────────────────────────────────────────

// sqliteModel is the app over a real store holding tasks.
func sqliteModel(t *testing.T, tasks ...todo.Todo) model {
	t.Helper()
	setTestHome(t, t.TempDir())
	testStore(t)
	if len(tasks) > 0 {
		if err := newSQLiteRepo().Save(todoPtrs(tasks), nil); err != nil {
			t.Fatal(err)
		}
	}
	m := initialModel(newSQLiteRepo())
	m.termWidth, m.termHeight = 100, 30
	m.refreshCaches()
	return m
}

// runSave runs the save the debounce would, off the loop, and hands its
// result back.
func runSave(t *testing.T, m model) model {
	t.Helper()
	m.savePending = true
	next, cmd := m.handleSaveTick()
	m = next.(model)
	for _, msg := range runCmd(cmd) {
		switch msg.(type) {
		case saveDoneMsg, saveErrMsg:
			m, _ = send(t, m, msg)
		}
	}
	return m
}

// editElsewhere changes the stored task id as a sync merge would, behind
// the app's back.
func editElsewhere(t *testing.T, id string, edit func(*todo.Todo)) {
	t.Helper()
	all, err := loadTodosForSync(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := range all {
		if all[i].ID == id {
			edit(&all[i])
			all[i].ModifiedAt = time.Now()
			if err := saveStamped(db, []*todo.Todo{&all[i]}, nil, func(*todo.Todo) float64 { return 0 }, time.Now(), editor{name: "Anna"}); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no stored task %s", id)
}

func storedRow(t *testing.T, id string) todo.Todo {
	t.Helper()
	all, err := loadTodosForSync(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range all {
		if x.ID == id {
			return x
		}
	}
	t.Fatalf("no stored task %s", id)
	return todo.Todo{}
}

func bumpPriority(m *model, id string) {
	m.pushUndo("priority", id)
	m.cyclePriority(m.get(id), 1)
	m.markModified(id)
}

// A sync merged into the store while the app held an older copy: the app
// missed the reload (the watcher took it for its own save) and the user then
// edits the task. The save writes the edit and keeps what the sync brought,
// the comment included, which saving the copy whole would have tombstoned;
// and the app shows both after it.
func TestASaveKeepsWhatOthersChangedSinceTheLoad(t *testing.T) {
	task := todo.New("Book the ferry")
	m := sqliteModel(t, task)
	editElsewhere(t, task.ID, func(x *todo.Todo) {
		x.Title = "Book the 9:00 ferry"
		x.AddComment("cabin 12 is free")
	})

	bumpPriority(&m, task.ID)
	m = runSave(t, m)

	got := storedRow(t, task.ID)
	if got.Title != "Book the 9:00 ferry" || got.Priority == task.Priority {
		t.Errorf("stored title %q, priority %v: want the sync's title and the edit's priority", got.Title, got.Priority)
	}
	if len(got.Comments) != 1 || !got.Comments[0].DeletedAt.IsZero() {
		t.Errorf("stored comments %+v: want the sync's comment, live", got.Comments)
	}
	if mine := m.get(task.ID); mine.Title != "Book the 9:00 ferry" || len(mine.Comments) != 1 {
		t.Errorf("the app shows %q with %d comments after the save", mine.Title, len(mine.Comments))
	}
}

// An edit to a task deleted elsewhere meanwhile does not bring it back, as a
// sync would also decide, and the app stops showing it.
func TestAnEditDoesNotUndoADeleteElsewhere(t *testing.T) {
	task := todo.New("Book the ferry")
	m := sqliteModel(t, task)
	if err := saveStamped(db, nil, map[string]time.Time{task.ID: time.Now()}, func(*todo.Todo) float64 { return 0 }, time.Now(), editor{name: "Anna"}); err != nil {
		t.Fatal(err)
	}
	bumpPriority(&m, task.ID)
	m = runSave(t, m)
	if got := storedRow(t, task.ID); !got.Deleted {
		t.Error("the edit brought back a task deleted elsewhere")
	}
	if m.get(task.ID) != nil {
		t.Error("the app still shows the deleted task")
	}
}

// A reload that lands while a task is still being edited takes the edit onto
// what was loaded, so the screen shows both and the save writes both.
func TestAReloadUnderAnEditKeepsTheEdit(t *testing.T) {
	task := todo.New("Book the ferry")
	m := sqliteModel(t, task)
	bumpPriority(&m, task.ID)
	editElsewhere(t, task.ID, func(x *todo.Todo) { x.Title = "Book the 9:00 ferry" })

	todos, err := m.repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	m, _ = send(t, m, reloadedMsg{todos: todos, epoch: m.saveEpoch})
	if mine := m.get(task.ID); mine.Title != "Book the 9:00 ferry" || mine.Priority == task.Priority {
		t.Errorf("after the reload the app shows %q at %v, want both changes", mine.Title, mine.Priority)
	}
	m = runSave(t, m)
	if got := storedRow(t, task.ID); got.Title != "Book the 9:00 ferry" || got.Priority == task.Priority {
		t.Errorf("stored %q at %v, want both changes", got.Title, got.Priority)
	}
}

// An undo after the edit was saved writes the undone field back, and an
// undo of a delete brings the task back.
func TestUndoWritesBackThroughTheStore(t *testing.T) {
	task := todo.New("Book the ferry")
	m := sqliteModel(t, task)
	bumpPriority(&m, task.ID)
	m = runSave(t, m)
	m = sendKey(t, m, "u")
	m = runSave(t, m)
	if got := storedRow(t, task.ID); got.Priority != task.Priority {
		t.Errorf("after undo the store holds priority %v, want %v", got.Priority, task.Priority)
	}

	m.tab = tabTasks
	m.refreshCaches()
	m = script(t, m, "x", "y")
	m = runSave(t, m)
	if got := storedRow(t, task.ID); !got.Deleted {
		t.Fatal("the delete was not stored")
	}
	m = sendKey(t, m, "u")
	m = runSave(t, m)
	if got := storedRow(t, task.ID); got.Deleted {
		t.Error("undoing the delete did not bring the task back in the store")
	}
}

// A reload read before a save started may lack what the save wrote, and
// would put the edit back to before it; it is read again instead. One asked
// for while a save runs waits for it.
func TestAReloadWaitsForTheSave(t *testing.T) {
	task := todo.New("Book the ferry")
	m := sqliteModel(t, task)
	stale, _ := m.repo.Load()
	bumpPriority(&m, task.ID)
	m.beginSave()

	if cmd := m.reloadCmd(); cmd != nil || !m.reloadWanted {
		t.Fatal("a reload asked for during a save did not wait for it")
	}
	m, cmd := send(t, m, reloadedMsg{todos: stale, epoch: m.saveEpoch - 1})
	if m.get(task.ID).Priority == task.Priority {
		t.Error("a read from before the save replaced the edit")
	}
	if cmd != nil {
		t.Error("the stale read asked for another while the save still runs")
	}
	if cmd := m.saveFinished(); len(runCmd(cmd)) == 0 {
		t.Error("the save's end did not run the reload that waited for it")
	}
}

// Saves run one at a time: a tick while one runs drains nothing, and the
// save's end schedules the next.
func TestSavesRunOneAtATime(t *testing.T) {
	task := todo.New("Book the ferry")
	m := sqliteModel(t, task)
	m.beginSave()
	bumpPriority(&m, task.ID)
	m.savePending = true
	next, cmd := m.handleSaveTick()
	m = next.(model)
	if cmd != nil || len(m.dirtyIDs) == 0 || !m.savePending {
		t.Fatal("a save started while another ran")
	}
	if m.saveFinished() == nil || !m.saveScheduled {
		t.Error("the running save's end did not schedule the deferred one")
	}
}

// editKeys keep the monkey on the task lists and the detail pane, editing:
// the general alphabet mostly wanders between tabs and modals.
var editKeys = []string{
	"up", "down", "home", "end", "left", "right", "enter", "esc", "1",
	"d", "t", "p", "x", "y", "u", "r", "T", "a", "n",
	"milk #home @House", "renamed", "45m", "#work", "backspace",
	"#", "work", "home", "@", "House", "space",
}

// What the app shows is what it stores: random edits over a real store, each
// followed by its save, and with nothing else writing, each save must store
// every live task as the screen showed it before the save, and leave the
// screen as it was. Comparing after the save alone would prove nothing, since
// the save's result is taken back into memory (adoptSaved).
func TestMonkeyEditsAreStoredAsShown(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			fm := monkeyModel(t)
			var tasks []todo.Todo
			for _, x := range fm.allTodos() {
				tasks = append(tasks, copyTodo(*x))
			}
			m := sqliteModel(t, tasks...)
			trail := ""
			for step := 0; step < 150; step++ {
				k := editKeys[rng.Intn(len(editKeys))]
				m = sendKey(t, m, k)
				trail += " " + k
				if m.mode != modeNormal {
					m = runSave(t, m)
					continue // a modal may hold edits it has not made yet
				}
				shown := shownTasks(m)
				m = runSave(t, m)
				where := fmt.Sprintf("seed %d after%s", seed, trail)
				checkStored(t, shown, where)
				checkShown(t, m, shown, where)
				checkReloadIsANoop(t, m, where)
			}
		})
	}
}

// checkReloadIsANoop asserts the store as just saved reads back as no change
// (sameAsLoaded through the bases), so a reload after the app's own save
// never costs a rebuild.
func checkReloadIsANoop(t *testing.T, m model, trail string) {
	t.Helper()
	if len(m.dirtyIDs) > 0 || len(m.tombstones) > 0 {
		return
	}
	loaded, err := m.repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !m.sameAsLoaded(loaded, 0) {
		t.Fatalf("%s: the store as just saved reads back as a change", trail)
	}
}

// shownTasks is a copy of every live task in memory.
func shownTasks(m model) map[string]todo.Todo {
	out := make(map[string]todo.Todo, len(m.tasks))
	for id, x := range m.tasks {
		out[id] = normalized(*x)
	}
	return out
}

func checkStored(t *testing.T, shown map[string]todo.Todo, trail string) {
	t.Helper()
	stored, err := loadTodosFromDB(db)
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]bool{}
	for i := range stored {
		live[stored[i].ID] = true
		want, ok := shown[stored[i].ID]
		if !ok {
			t.Fatalf("%s: %q is stored live but was not shown", trail, stored[i].Title)
		}
		got := normalized(stored[i])
		if !sameContent(&want, &got) {
			t.Fatalf("%s: %q was shown and stored differently in %s", trail, want.Title, contentDiff(&want, &got))
		}
	}
	for id, x := range shown {
		if !live[id] {
			t.Fatalf("%s: %q was shown but not stored live", trail, x.Title)
		}
	}
}

func checkShown(t *testing.T, m model, before map[string]todo.Todo, trail string) {
	t.Helper()
	after := shownTasks(m)
	if len(after) != len(before) {
		t.Fatalf("%s: the save changed the screen from %d tasks to %d", trail, len(before), len(after))
	}
	for id, want := range before {
		got := after[id]
		if !sameContent(&want, &got) {
			t.Fatalf("%s: the save changed %q on screen in %s", trail, want.Title, contentDiff(&want, &got))
		}
	}
}

// normalized is t with its sets and records in one order, which the store
// does not keep.
func normalized(t todo.Todo) todo.Todo {
	t = copyTodo(t)
	sort.Strings(t.Tags)
	sort.Strings(t.Dependencies)
	sort.Slice(t.Comments, func(i, j int) bool { return t.Comments[i].ID < t.Comments[j].ID })
	sort.Slice(t.TimeEntries, func(i, j int) bool { return t.TimeEntries[i].ID < t.TimeEntries[j].ID })
	return t
}

// contentDiff names what a and b disagree on, with both values.
func contentDiff(a, b *todo.Todo) string {
	var out []string
	for _, f := range todo.Fields {
		if !f.Same(a, b) {
			var x, y todo.Todo
			f.Copy(&x, a)
			f.Copy(&y, b)
			out = append(out, fmt.Sprintf("%s (%+v vs %+v)", f.Key, x, y))
		}
	}
	if !slices.Equal(a.Tags, b.Tags) {
		out = append(out, fmt.Sprintf("tags (%v vs %v)", a.Tags, b.Tags))
	}
	if !slices.Equal(a.Dependencies, b.Dependencies) {
		out = append(out, fmt.Sprintf("dependencies (%v vs %v)", a.Dependencies, b.Dependencies))
	}
	if !slices.EqualFunc(a.Comments, b.Comments, sameComment) {
		out = append(out, fmt.Sprintf("comments (%+v vs %+v)", a.Comments, b.Comments))
	}
	if !slices.EqualFunc(a.TimeEntries, b.TimeEntries, sameTimeEntry) {
		out = append(out, fmt.Sprintf("time entries (%+v vs %+v)", a.TimeEntries, b.TimeEntries))
	}
	return strings.Join(out, "; ")
}

// A sync merge that lands right after the app's own save reaches the screen:
// the watcher's signal is looked into and the store read, where a guess by
// time took it for the save. The save's own signal costs no read.
func TestAMergeRightAfterASaveReachesTheScreen(t *testing.T) {
	task := todo.New("Book the ferry")
	m := sqliteModel(t, task)
	m.watcher = newWatcherState()
	m.watcher.unseenWrite(storeDataVersion)

	bumpPriority(&m, task.ID)
	m = runSave(t, m)
	if msgs := runCmd(m.reloadIfChangedCmd()); len(msgs) != 0 {
		t.Fatalf("the app's own save made the watcher read the store: %v", msgs)
	}

	theirs := storedRow(t, task.ID)
	theirs.Title = "Book the 9:00 ferry"
	theirs.Stamps["title"] = hlc.At(time.Now().Add(time.Minute))
	if _, _, err := mergeIntoStore(db, []todo.Todo{theirs}, rank.Biases{}); err != nil {
		t.Fatal(err)
	}
	msgs := runCmd(m.reloadIfChangedCmd())
	if len(msgs) != 1 {
		t.Fatalf("the merge's signal produced %v, want the store read", msgs)
	}
	m, _ = send(t, m, msgs[0])
	if got := m.get(task.ID); got.Title != "Book the 9:00 ferry" || got.Priority == task.Priority {
		t.Errorf("the app shows %q at %v, want the merged title and its own priority", got.Title, got.Priority)
	}
}

// A reload that brings nothing new is told apart without hashing memory: the
// bases carry the store's version of each task, a save's result included,
// which must hash as the next load of it does. A merge since is a change.
func TestAnUnchangedReloadIsKnownByItsBases(t *testing.T) {
	task := todo.New("Book the ferry")
	task.AddComment("cabin 12")
	m := sqliteModel(t, task, todo.New("Pack"))
	bumpPriority(&m, task.ID)
	m = runSave(t, m)
	if len(m.versions) != len(m.tasks) {
		t.Fatalf("%d of %d tasks have a base version", len(m.versions), len(m.tasks))
	}

	loaded, err := m.repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !m.sameAsLoaded(loaded, 0) {
		t.Error("the store as just saved was taken for a change")
	}
	theirs := storedRow(t, task.ID)
	theirs.Title = "Book the 9:00 ferry"
	theirs.Stamps["title"] = hlc.At(time.Now().Add(time.Minute))
	if _, _, err := mergeIntoStore(db, []todo.Todo{theirs}, rank.Biases{}); err != nil {
		t.Fatal(err)
	}
	if loaded, _ = m.repo.Load(); m.sameAsLoaded(loaded, 0) {
		t.Error("a merge since the save was taken for no change")
	}
}
