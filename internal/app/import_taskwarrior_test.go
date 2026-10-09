package app

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/todo"
)

// twSample is a `task export` as Taskwarrior 3 writes it, with one of each
// thing the import has to handle: a pending task with everything set, a
// completed one it depends on, a deleted one, a recurring series (template,
// two pending instances, one completed), a waiting task, one put off until
// someday, one scheduled, one started, a depends in the old comma-separated
// form, an until, and two UDAs.
const twSample = `[
{"id":1,"description":"buy a new bike","entry":"20260901T080000Z","modified":"20260905T100000Z","status":"pending","uuid":"11111111-1111-4111-8111-111111111111","project":"Home.Garage","tags":["errand","Big Spend"],"priority":"H","due":"20261020T220000Z","depends":["22222222-2222-4222-8222-222222222222","99999999-9999-4999-8999-999999999999"],"annotations":[{"entry":"20260902T090000Z","description":"ask about the frame size"}],"estimate":"3h","urgency":12.3},
{"id":0,"description":"measure the garage","entry":"20260901T080000Z","modified":"20260903T100000Z","end":"20260903T100000Z","status":"completed","uuid":"22222222-2222-4222-8222-222222222222","priority":"L"},
{"id":0,"description":"old idea","entry":"20260801T080000Z","modified":"20260802T080000Z","end":"20260802T080000Z","status":"deleted","uuid":"99999999-9999-4999-8999-999999999999"},
{"id":0,"description":"water the plants","entry":"20260901T080000Z","modified":"20260901T080000Z","status":"recurring","uuid":"33333333-3333-4333-8333-333333333333","recur":"weekly","due":"20260907T220000Z","rtype":"periodic","mask":"+--"},
{"id":2,"description":"water the plants","entry":"20260908T080000Z","modified":"20260908T080000Z","status":"pending","uuid":"33333333-3333-4333-8333-000000000001","parent":"33333333-3333-4333-8333-333333333333","recur":"weekly","imask":1,"due":"20260914T220000Z"},
{"id":3,"description":"water the plants","entry":"20260915T080000Z","modified":"20260915T080000Z","status":"pending","uuid":"33333333-3333-4333-8333-000000000002","parent":"33333333-3333-4333-8333-333333333333","recur":"weekly","imask":2,"due":"20260921T220000Z"},
{"id":0,"description":"water the plants","entry":"20260901T080000Z","modified":"20260908T070000Z","end":"20260908T070000Z","status":"completed","uuid":"33333333-3333-4333-8333-000000000000","parent":"33333333-3333-4333-8333-333333333333","recur":"weekly","imask":0,"due":"20260907T220000Z"},
{"id":4,"description":"renew the passport","entry":"20260901T080000Z","modified":"20260901T080000Z","status":"waiting","uuid":"44444444-4444-4444-8444-444444444444","wait":"20261101T230000Z","depends":"11111111-1111-4111-8111-111111111111,22222222-2222-4222-8222-222222222222"},
{"id":5,"description":"learn the cello","entry":"20260901T080000Z","modified":"20260901T080000Z","status":"waiting","uuid":"55555555-5555-4555-8555-555555555555","wait":"99991230T000000Z"},
{"id":6,"description":"file the taxes","entry":"20260901T080000Z","modified":"20260901T080000Z","status":"pending","uuid":"66666666-6666-4666-8666-666666666666","scheduled":"20261015T220000Z","until":"20261231T230000Z","client":"self"},
{"id":7,"description":"write the report","entry":"20260901T080000Z","modified":"20260910T081500Z","status":"pending","uuid":"77777777-7777-4777-8777-777777777777","start":"20260910T081500Z","recur":"P1H"}
]`

func twByTitle(t *testing.T, tasks []todo.Todo, title string) []todo.Todo {
	t.Helper()
	var out []todo.Todo
	for _, x := range tasks {
		if x.Title == title {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no imported task titled %q", title)
	}
	return out
}

func TestTaskwarriorExportIsRecognised(t *testing.T) {
	if !isTaskwarriorExport([]byte(twSample)) {
		t.Fatal("a task export was not recognised")
	}
	tjek, err := exportJSON([]todo.Todo{todo.New("a tjek task")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"tjek envelope":   string(tjek),
		"tjek bare array": `[{"id":"x","title":"a tjek task"}]`,
		"empty array":     `[]`,
	} {
		if isTaskwarriorExport([]byte(data)) {
			t.Errorf("%s taken for a Taskwarrior export", name)
		}
	}
	file, err := readImport([]byte(twSample))
	if err != nil || !file.taskwarrior || len(file.tasks) == 0 {
		t.Fatalf("readImport: %v, taskwarrior=%v, %d tasks", err, file.taskwarrior, len(file.tasks))
	}
}

func TestTaskwarriorFieldsComeAcross(t *testing.T) {
	file, err := parseTaskwarrior([]byte(twSample))
	if err != nil {
		t.Fatal(err)
	}
	tasks, notes := file.tasks, file.notes
	// 11 entries: the template and the deleted task are left out.
	if len(tasks) != 9 {
		t.Fatalf("got %d tasks, want 9", len(tasks))
	}

	bike := twByTitle(t, tasks, "Buy a new bike")[0]
	if bike.ID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("ID = %q, want the Taskwarrior UUID", bike.ID)
	}
	if bike.Priority != todo.PriorityHigh || bike.Project != "Home.Garage" {
		t.Errorf("priority %v, project %q", bike.Priority, bike.Project)
	}
	if !reflect.DeepEqual(bike.Tags, []string{"errand", "big-spend"}) {
		t.Errorf("tags = %v", bike.Tags)
	}
	wantDue := localDay(time.Date(2026, 10, 20, 22, 0, 0, 0, time.UTC))
	if !bike.DueDate.Equal(wantDue) {
		t.Errorf("due = %v, want %v", bike.DueDate, wantDue)
	}
	if !reflect.DeepEqual(bike.Dependencies, []string{"22222222-2222-4222-8222-222222222222"}) {
		t.Errorf("dependencies = %v; the link to the deleted task should go", bike.Dependencies)
	}
	if len(bike.Comments) != 1 || bike.Comments[0].Text != "ask about the frame size" ||
		!bike.Comments[0].CreatedAt.Equal(time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("comments = %+v", bike.Comments)
	}
	if !bike.CreatedAt.Equal(time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)) ||
		!bike.ModifiedAt.Equal(time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("created %v, modified %v", bike.CreatedAt, bike.ModifiedAt)
	}

	measure := twByTitle(t, tasks, "Measure the garage")[0]
	if measure.Status != todo.Done || !measure.CompletedAt.Equal(time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)) ||
		measure.Priority != todo.PriorityLow {
		t.Errorf("completed task: %+v", measure)
	}

	// The rule goes on the later pending instance only; the completed one is
	// history.
	plants := twByTitle(t, tasks, "Water the plants")
	if len(plants) != 3 {
		t.Fatalf("got %d plant tasks, want 3 instances", len(plants))
	}
	for _, p := range plants {
		want := ""
		if p.ID == "33333333-3333-4333-8333-000000000002" {
			want = "weekly"
		}
		if p.Recurrence != want {
			t.Errorf("instance %s: recurrence %q, want %q", p.ID, p.Recurrence, want)
		}
	}

	passport := twByTitle(t, tasks, "Renew the passport")[0]
	if passport.Status != todo.Pending || passport.StartDate.IsZero() || len(passport.Dependencies) != 2 {
		t.Errorf("waiting task: status %v, start %v, deps %v", passport.Status, passport.StartDate, passport.Dependencies)
	}
	if !todo.IsSomeday(twByTitle(t, tasks, "Learn the cello")[0].StartDate) {
		t.Error("wait:someday should become a someday start")
	}
	taxes := twByTitle(t, tasks, "File the taxes")[0]
	if want := localDay(time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC)).Add(9 * time.Hour); !taxes.StartDate.Equal(want) {
		t.Errorf("scheduled → start %v, want %v", taxes.StartDate, want)
	}
	report := twByTitle(t, tasks, "Write the report")[0]
	if !report.StartDate.Equal(time.Date(2026, 9, 10, 8, 15, 0, 0, time.UTC)) {
		t.Errorf("start → start date %v", report.StartDate)
	}

	all := strings.Join(notes, "\n")
	for _, want := range []string{"1 deleted", "until:", "client, estimate", "1 dependency link"} {
		if !strings.Contains(all, want) {
			t.Errorf("notes miss %q:\n%s", want, all)
		}
	}
}

func TestTaskwarriorRecurRules(t *testing.T) {
	for in, want := range map[string]string{
		"daily": "daily", "weekly": "weekly", "biweekly": "2w", "fortnight": "2w",
		"monthly": "monthly", "quarterly": "3m", "semiannual": "6m", "annual": "yearly",
		"weekdays": "weekdays", "3d": "3d", "2 weeks": "2w", "1mo": "monthly", "2q": "6m",
		"P1W": "weekly", "p3d": "3d",
	} {
		got, ok := twRecur(in)
		canon, _ := todo.ParseRecurrence(want)
		if !ok || got != canon {
			t.Errorf("twRecur(%q) = %q, %v; want %q", in, got, ok, canon)
		}
	}
	for _, in := range []string{"P1H", "5min", "1m", "", "0d", "hourly"} {
		if got, ok := twRecur(in); ok {
			t.Errorf("twRecur(%q) = %q, want no rule", in, got)
		}
	}
}

// The same export read twice gives the same tasks, so a second import into
// the store changes nothing.
func TestTaskwarriorImportIsIdempotent(t *testing.T) {
	firstFile, err := parseTaskwarrior([]byte(twSample))
	if err != nil {
		t.Fatal(err)
	}
	secondFile, _ := parseTaskwarrior([]byte(twSample))
	first, second := firstFile.tasks, secondFile.tasks
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two reads of one export differ")
	}
	h := openTestDB(t)
	if _, changed, err := mergeIntoStore(h, first, rank.DefaultBiases()); err != nil || !changed {
		t.Fatalf("first import: changed=%v, %v", changed, err)
	}
	if _, changed, err := mergeIntoStore(h, second, rank.DefaultBiases()); err != nil || changed {
		t.Fatalf("second import: changed=%v, %v; want no change", changed, err)
	}
}

// The app's toast names what did not come across, after the undo hint.
func TestTaskwarriorImportToastSaysWhatWasNotKept(t *testing.T) {
	file, err := parseTaskwarrior([]byte(twSample))
	if err != nil {
		t.Fatal(err)
	}
	m := modelWithTasks(t)
	next, _ := m.handleImportDone(importDoneMsg{res: importResult{added: 9}, left: file.left})
	got := next.(model).err
	for _, want := range []string{"u undoes it · not kept: 1 deleted", "until", "client, estimate"} {
		if !strings.Contains(got, want) {
			t.Errorf("toast %q lacks %q", got, want)
		}
	}
}
