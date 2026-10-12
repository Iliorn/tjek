package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/todo"
	"github.com/charmbracelet/x/ansi"
)

// TestMain (main_test.go) already redirects $HOME to a temp dir, so
// settingsPath() points into a sandbox and these tests can write/read
// settings.json freely without touching the user's real config.

func TestLoadSettingsMissingFileNoError(t *testing.T) {
	// Brand-new install: settings.json doesn't exist yet.
	os.Remove(settingsPath())
	s, err := loadSettings()
	if err != nil {
		t.Fatalf("missing file should not error, got: %v", err)
	}
	if !reflect.DeepEqual(s, appSettings{}) {
		t.Errorf("expected zero appSettings on missing file, got %+v", s)
	}
}

func TestLoadSettingsCorruptFileErrors(t *testing.T) {
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(settingsPath(), []byte("{not json"), 0644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	defer os.Remove(settingsPath())

	_, err := loadSettings()
	if err == nil {
		t.Fatal("corrupt JSON should return an error, got nil")
	}
}

// TestLoadSettingsMigratesLegacyVersion0 mirrors the task-file pattern: a
// settings.json written before the Version field existed has Version=0 in
// Go's decoder, and migrateSettings must bring it up to current without
// dropping the user's preferences.
func TestLoadSettingsMigratesLegacyVersion0(t *testing.T) {
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `{"theme":"tokyonight","task_sort":1}`
	if err := os.WriteFile(settingsPath(), []byte(legacy), 0644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	defer os.Remove(settingsPath())

	s, err := loadSettings()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.Version != currentSettingsVersion {
		t.Errorf("migrated Version = %d, want %d", s.Version, currentSettingsVersion)
	}
	if s.Theme != "tokyonight" {
		t.Errorf("Theme lost during migration: %q", s.Theme)
	}
	if s.TaskSort != taskSortDueDate {
		t.Errorf("TaskSort lost: %v", s.TaskSort)
	}
}

// v2 moved the Done column into the "stages" list as its last entry. A v1 file
// listed only the working columns, so reading one unchanged would promote its
// last one ("Review") to Done and file every card in it under completed work.
// The migration appends the column the v1 renderer drew — in the language it
// drew it in, since that is the heading the user was looking at.
func TestSettingsMigrationKeepsTheOldLastColumnWorking(t *testing.T) {
	cases := []struct {
		name    string
		version int
		lang    string
		in      []string
		want    []string
	}{
		{"v1 list gains the Done column", 1, "", []string{"Backlog", "In progress", "Review"},
			[]string{"Backlog", "In progress", "Review", "Done"}},
		{"legacy pre-version file too", 0, "", []string{"Todo", "Doing"},
			[]string{"Todo", "Doing", "Done"}},
		{"in the language the column was shown in", 1, "da", []string{"Todo"},
			[]string{"Todo", "Færdige"}},
		{"an unset list still means the defaults", 1, "", nil, nil},
	}
	for _, c := range cases {
		got := migrateSettings(c.version, appSettings{Version: c.version, Language: c.lang, Stages: c.in})
		if !reflect.DeepEqual(got.Stages, c.want) {
			t.Errorf("%s: stages = %v, want %v", c.name, got.Stages, c.want)
		}
		if got.Version != currentSettingsVersion {
			t.Errorf("%s: version = %d, want %d", c.name, got.Version, currentSettingsVersion)
		}
	}

	// End to end: the migrated list still has Review as a working column, so a
	// card sitting there stays pending work rather than joining the done pile.
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.Remove(settingsPath())
	v1 := `{"version":1,"stages":["Backlog","In progress","Review"]}`
	if err := os.WriteFile(settingsPath(), []byte(v1), 0644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	s, err := loadSettings()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	stages := stagesFromSettings(s)
	if want := []string{"Backlog", "In progress", "Review", "Done"}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("migrated board = %v, want %v", stages, want)
	}
	if _, ok := testBoard(stages).canonicalStage("Review"); !ok {
		t.Error("Review stopped being a working column after the migration")
	}

	// A file already on v2 is left alone — migrating twice would append a
	// second Done column every launch.
	twice := migrateSettings(currentSettingsVersion, appSettings{Version: currentSettingsVersion, Stages: []string{"Todo", "Done"}})
	if want := []string{"Todo", "Done"}; !reflect.DeepEqual(twice.Stages, want) {
		t.Errorf("re-migrated stages = %v, want %v", twice.Stages, want)
	}
}

// TestSaveSettingsStampsVersion confirms saveSettings always writes the
// current schema version, even if the caller forgot to set it.
func TestSaveSettingsStampsVersion(t *testing.T) {
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.Remove(settingsPath())

	in := appSettings{Theme: "test", SeqBiasDeadline: rank.Intense}
	if err := saveSettings(in); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Re-read the raw JSON to confirm the version is on disk (not just an
	// in-memory side effect of loadSettings).
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	var onDisk appSettings
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if onDisk.Version != currentSettingsVersion {
		t.Errorf("on-disk version = %d, want %d", onDisk.Version, currentSettingsVersion)
	}
	if onDisk.Theme != "test" || onDisk.SeqBiasDeadline != rank.Intense {
		t.Errorf("payload lost on round-trip: %+v", onDisk)
	}
}

// TestSettingsTopPreviewAppearsInView checks that the bias-knob preview block
// is present when the Settings tab is rendered with at least one pending task.
func TestSettingsTopPreviewAppearsInView(t *testing.T) {
	tasks := []todo.Todo{
		mkTodo("t1", "Alpha task", todo.Pending),
		mkTodo("t2", "Beta task", todo.Pending),
	}
	m := modelWithTasks(t, tasks...)
	m.tab = tabSettings
	m.taskSort = taskSortSequence
	m.settingsCursor = settingBiasDeadline
	m.ensureCache()

	out := m.renderSettingsList()
	if !strings.Contains(out, "Top 5 with these weights:") {
		t.Errorf("Settings view should contain preview header, got:\n%s", out)
	}
}

// TestSettingsTopPreviewEmptyWhenNoTasks checks that the preview is absent
// (no header, no rows) when there are no pending tasks.
func TestSettingsTopPreviewEmptyWhenNoTasks(t *testing.T) {
	m := modelWithTasks(t)
	m.tab = tabSettings
	m.ensureCache()

	out := m.renderSettingsList()
	if strings.Contains(out, "Top 5 with these weights:") {
		t.Errorf("Settings view should not show preview header when there are no tasks:\n%s", out)
	}
}

// Settings is one pane: the section bar names every group, and the page
// below it is the cursor's group, the Sequencer's under its own name.
func TestSettingsRendersOneGroupedPane(t *testing.T) {
	m := modelWithTasks(t, todo.New("Ranked task"))
	m.tab = tabSettings
	m.termHeight = 40
	m.settingsCursor = settingBiasDeadline
	m.ensureCache()
	content, _ := m.renderSettingsSection(50)
	content = ansi.Strip(content)
	if !strings.Contains(content, "Deadline pressure") {
		t.Fatalf("the Sequencer page is missing its rows:\n%s", content)
	}

	for _, width := range []int{60, 120} {
		m.termWidth = width
		out := ansi.Strip(m.View())
		if strings.Contains(out, "╭─ "+tr("Sequencer")+" ") {
			t.Errorf("width=%d: Settings should draw one pane, not a Sequencer pane:\n%s", width, out)
		}
		if !strings.Contains(out, "╭─ "+tr("Preferences")+" ") {
			t.Errorf("width=%d: Settings should draw its single pane:\n%s", width, out)
		}
		// A narrow bar keeps the current name and as many neighbours as fit.
		wants := []string{tr("Sequencer"), "Deadline pressure"}
		if width >= 120 {
			wants = append(wants, tr("Appearance"), tr("Sync"), tr("About"))
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("width=%d: the pane should show %q:\n%s", width, want, out)
			}
		}
	}
}

// The pane scrolls to the cursor, and "visible" has to mean visible in what
// the panel actually draws: its chrome is two borders *and* the padding row
// above the first line, so a height that counted only the borders pushed the
// selected row off the bottom on a short terminal.
func TestSettingsPaneKeepsTheSelectedRowOnScreen(t *testing.T) {
	m := modelWithTasks(t, todo.New("Ranked task"))
	m.tab = tabSettings
	m.ensureCache()
	for _, h := range []int{12, 16, 20, 24, 30} {
		m.termWidth, m.termHeight = 70, h
		for _, row := range m.settingsNavOrder() {
			m.settingsCursor = row
			out := ansi.Strip(m.View())
			found := false
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, cursorMark) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("height=%d row=%d: the selected row is not on screen:\n%s", h, row, out)
			}
		}
	}
}

// TestSettingsTopPreviewRespectsWeights verifies that the preview ranks tasks
// in the order the rank.Biases imply: with Priority=Intense and Deadline=Relaxed,
// a high-priority task with no due date should rank above a medium-priority
// task with an imminent due date when the preview is computed directly via
// rank.TopWith.
func TestSettingsTopPreviewRespectsWeights(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	highPri := mkTodo("hp", "High priority, no deadline", todo.Pending)
	highPri.Priority = todo.PriorityHigh
	// No due date — Deadline dimension contributes 0.

	dueUrgent := mkTodo("du", "Medium priority, urgent deadline", todo.Pending)
	dueUrgent.Priority = todo.PriorityMedium
	dueUrgent.DueDate = now.AddDate(0, 0, 1) // due tomorrow (7 days window → ~8.9 urgency)

	todos := []todo.Todo{highPri, dueUrgent}
	heat := rank.Heat{}

	// With Priority=Intense (2×) and Deadline=Relaxed (0.5×):
	// highPri:  Priority(10) × 2.0 = 20 + small age
	// dueUrgent: Priority(5) × 2.0 + Urgency(~8.9) × 0.5 = 10 + ~4.45 ≈ 14.45
	// → highPri should rank first.
	intensePri := rank.Biases{
		Priority: rank.Intense,
		Deadline: rank.Relaxed,
		Momentum: rank.Balanced,
		Aging:    true,
	}
	ranked := rank.TopWith(todoPtrs(todos), intensePri, heat, now)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked tasks, got %d", len(ranked))
	}
	if ranked[0].ID != "hp" {
		t.Errorf("Priority=Intense ranking: expected high-priority task first, got %q (score order: %s, %s)",
			ranked[0].ID,
			fmt.Sprintf("%.2f", rank.ComponentsAt(now, &ranked[0], intensePri, heat).Total),
			fmt.Sprintf("%.2f", rank.ComponentsAt(now, &ranked[1], intensePri, heat).Total),
		)
	}

	// With Deadline=Intense (2×) and Priority=Relaxed (0.5×):
	// highPri:  Priority(10) × 0.5 = 5 + small age
	// dueUrgent: Priority(5) × 0.5 + Urgency(~8.9) × 2.0 = 2.5 + ~17.8 ≈ 20.3
	// → dueUrgent should rank first.
	intenseDeadline := rank.Biases{
		Priority: rank.Relaxed,
		Deadline: rank.Intense,
		Momentum: rank.Balanced,
		Aging:    true,
	}
	ranked2 := rank.TopWith(todoPtrs(todos), intenseDeadline, heat, now)
	if len(ranked2) != 2 {
		t.Fatalf("expected 2 ranked tasks, got %d", len(ranked2))
	}
	if ranked2[0].ID != "du" {
		t.Errorf("Deadline=Intense ranking: expected urgent-deadline task first, got %q (score order: %s, %s)",
			ranked2[0].ID,
			fmt.Sprintf("%.2f", rank.ComponentsAt(now, &ranked2[0], intenseDeadline, heat).Total),
			fmt.Sprintf("%.2f", rank.ComponentsAt(now, &ranked2[1], intenseDeadline, heat).Total),
		)
	}
}

// TestSettingsTopPreviewNoWrap guards the no-wrap contract: the Settings view
// output must not exceed termWidth on any line, even with the preview block.
func TestSettingsTopPreviewNoWrap(t *testing.T) {
	for _, width := range []int{40, 60, 80, 100, 120} {
		tasks := []todo.Todo{
			mkTodo("t1", "Task one with a rather long name for testing layout", todo.Pending),
			mkTodo("t2", "Task two also somewhat verbose", todo.Pending),
			mkTodo("t3", "Task three", todo.Pending),
		}
		m := modelWithTasks(t, tasks...)
		m.tab = tabSettings
		m.termWidth = width
		m.termHeight = 40
		m.ensureCache()

		out := m.View()
		for n, line := range strings.Split(out, "\n") {
			if lw := ansi.StringWidth(line); lw > width {
				t.Errorf("width=%d: line %d is %d cells wide: %q", width, n, lw, line)
			}
		}
	}
}

// The sync footer wraps instead of being cut, and wrapping must not break the
// pane's no-wrap contract: a long server error is exactly the string that
// would otherwise run past the border.
func TestSettingsFooterWrapsTheSyncStatus(t *testing.T) {
	m := modelWithTasks(t, todo.New("Ranked task"))
	m.tab = tabSettings
	m.termHeight = 40
	m.ensureCache()
	m.syncStatus = "Last sync failed: sync server runs tjek v1.25.0, this device runs v1.33.1 — restart the sync server (it answered 500 Internal Server Error: merge failed: no such table: task_learnings)"

	const paneW = 50
	preferences, _ := m.renderSettingsSection(paneW)
	preferences = ansi.Strip(preferences)

	// Only the footer's own lines: this function deliberately leaves the final
	// per-line width contract to the pane builder, so the settings rows above
	// are not this test's business.
	all := strings.Split(strings.TrimRight(preferences, "\n"), "\n")
	start := -1
	for i, line := range all {
		if strings.Contains(line, "Last sync failed:") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no sync status in the footer:\n%s", preferences)
	}
	footer := all[start:]
	if len(footer) < 2 {
		t.Errorf("the message should have wrapped, got one line: %q", footer[0])
	}
	if len(footer) > syncStatusMaxLines {
		t.Errorf("footer runs %d lines, over the %d-line cap", len(footer), syncStatusMaxLines)
	}
	for _, line := range footer {
		if w := ansi.StringWidth(line); w > paneW {
			t.Errorf("footer line is %d wide, over the %d-column pane: %q", w, paneW, line)
		}
	}
	// Read back across the wrap: the part a fixed-width truncation used to eat.
	flat := strings.Join(strings.Fields(strings.Join(footer, " ")), " ")
	if !strings.Contains(flat, "restart the sync server") {
		t.Errorf("footer lost the actionable half of the message:\n%s", preferences)
	}
}

// clampLines is the cap that keeps a pathological server message from pushing
// the settings rows off the pane, and a capped block has to be tellable from
// one that simply ended.
func TestClampLinesMarksTheCut(t *testing.T) {
	lines := []string{"one", "two", "three", "four"}
	got := clampLines(lines, 2)
	if len(got) != 2 {
		t.Fatalf("clampLines returned %d lines, want 2", len(got))
	}
	if !strings.HasSuffix(got[1], ellipsis) {
		t.Errorf("last line %q should carry the ellipsis", got[1])
	}
	if same := clampLines(lines, 9); len(same) != len(lines) || strings.HasSuffix(same[3], ellipsis) {
		t.Errorf("an uncut block must be returned untouched, got %q", same)
	}
	if clampLines(lines, 0) != nil {
		t.Error("clampLines with no room should return nothing")
	}
}

// The pane is a page per group: the cursor's group and nothing else, so the
// section bar above it names what is on screen, and ←/→ turn the page.
func TestSettingsPaneShowsTheCursorsGroup(t *testing.T) {
	m := settingsModel(t)
	// One row label only its own group has, for each group.
	marker := map[string]string{
		"Appearance":      "Theme",
		"General":         "Your name",
		"Sequencer":       "Deadline pressure",
		"Sync":            "Server address",
		"Shared projects": "Join a project",
		"Server":          "Run server here",
		"Export":          "Auto-export folder",
		"About":           "Check for updates",
		"Columns":         "Last comment",
		"Keys":            "Merge tags",
	}
	for gi, g := range settingsGroups {
		if marker[g.title] == "" {
			t.Fatalf("group %q has no marker row in this test", g.title)
		}
		rows := m.visibleGroupRows(g)
		m.settingsCursor = rows[len(rows)-1]
		for _, w := range []int{50, 140} {
			content, _ := m.renderSettingsSection(w)
			content = ansi.Strip(content)
			for oi, other := range settingsGroups {
				if got := strings.Contains(content, tr(marker[other.title])); got != (oi == gi) {
					t.Errorf("width %d, page %q: %q shown = %v:\n%s", w, g.title, marker[other.title], got, content)
				}
			}
		}
	}
}

// A value too long for its row is clipped before the ⏎ is added, so the row
// still says that enter edits it — the list of board columns is the one that
// runs long.
func TestSettingsEditMarkSurvivesAClippedValue(t *testing.T) {
	m := modelWithTasks(t, todo.New("Ranked task"))
	m.tab = tabSettings
	m.termHeight = 40
	m.boardCfg.setStages([]string{"Backlog", "Waiting on someone else", "In progress", "In review with the team", "Done"})
	m.settingsCursor = settingStages
	m.ensureCache()
	for _, w := range []int{50, 80} {
		content, _ := m.renderSettingsSection(w)
		found := false
		for _, line := range strings.Split(content, "\n") {
			plain := ansi.Strip(line)
			if !strings.Contains(plain, tr("Board columns")) {
				continue
			}
			found = true
			if !strings.Contains(plain, "…") || !strings.Contains(plain, "⏎") {
				t.Errorf("width %d: the clipped row should end in … and keep its ⏎: %q", w, plain)
			}
			if cw := ansi.StringWidth(line); cw > w {
				t.Errorf("width %d: the row is %d cells wide", w, cw)
			}
		}
		if !found {
			t.Fatalf("width %d: no Board columns row in:\n%s", w, ansi.Strip(content))
		}
	}
}

// The Columns page has a row per column, and its rows run to the end of the
// settings IDs.
func TestColumnsPageCoversEveryColumn(t *testing.T) {
	if n := settingKeyFirst - settingColFirst; n != len(listColumnKeys) {
		t.Fatalf("the Columns page has %d row IDs for %d columns", n, len(listColumnKeys))
	}
	for _, k := range listColumnKeys {
		if listColumnLabels[k] == "" {
			t.Errorf("column %q has no label on the Columns page", k)
		}
	}
	for i, x := range extraColumns {
		if listColumnKeys[len(listColumnKeys)-len(extraColumns)+i] != x.key {
			t.Errorf("extra column %q is out of listColumnKeys order", x.key)
		}
	}
}

// enter on a Columns row switches the column in the Tasks list and keeps the
// choice in settings.json; a column the file does not name keeps its default.
func TestColumnsSwitchOnAndOff(t *testing.T) {
	task := todo.New("Paint the hall")
	task.AddComment("bought the paint")
	m := modelWithTasks(t, task)
	m.termWidth, m.termHeight = 160, 30
	m.tab = tabSettings
	m.ensureCache()
	if !m.columns["score"] || m.columns["comment"] {
		t.Fatalf("defaults = %v, want Score on and Last comment off", m.columns)
	}
	row := func(key string) int { return settingColFirst + slices.Index(listColumnKeys, key) }

	m.settingsCursor = row("comment")
	m = sendKey(t, m, "enter")
	m.settingsCursor = row("score")
	m = sendKey(t, m, "enter")
	m = sendKey(t, m, "1")
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, tr("Last comment")) || !strings.Contains(plain, "bought the paint") {
		t.Errorf("Last comment switched on but not drawn:\n%s", plain)
	}
	if strings.Contains(plain, listHeader("Score")) {
		t.Errorf("Score switched off but still drawn:\n%s", plain)
	}

	s, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Columns["comment"] || s.Columns["score"] {
		t.Errorf("settings.json columns = %v", s.Columns)
	}
	if got := listColumnsFromSettings(map[string]bool{"due": false}); got["due"] || !got["score"] {
		t.Errorf("an absent column should keep its default: %v", got)
	}

	// Every column on: they drop out rather than run past the pane.
	m.columns = allListColumns()
	for _, w := range []int{160, 120, 90, 60, 40} {
		m.termWidth = w
		for _, line := range strings.Split(m.View(), "\n") {
			if ansi.StringWidth(line) > w {
				t.Errorf("width %d: a line is %d wide: %q", w, ansi.StringWidth(line), ansi.Strip(line))
			}
		}
	}
}

// An extra column's width holds every value it can show in every language: a
// priority word, an age, a total of time, so none of them is clipped.
func TestExtraColumnsFitTheirValues(t *testing.T) {
	width := func(key string) int {
		for _, x := range extraColumns {
			if x.key == key {
				return x.w
			}
		}
		t.Fatalf("no extra column %q", key)
		return 0
	}
	now := time.Now()
	defer applyLang(string(activeLang))
	for _, lang := range availableLanguages {
		applyLang(string(lang))
		for _, p := range []todo.Priority{todo.PriorityLow, todo.PriorityMedium, todo.PriorityHigh} {
			if w := runeLen(trPriority(p)); w > width("priority") {
				t.Errorf("%s: priority %q is %d wide, over the column's %d", lang, trPriority(p), w, width("priority"))
			}
		}
		for _, ago := range []time.Duration{0, 5 * 24 * time.Hour, 60 * 24 * time.Hour, 900 * 24 * time.Hour} {
			if s := formatSince(now.Add(-ago), now); runeLen(s) > width("created") || runeLen(s) > width("changed") {
				t.Errorf("%s: age %q does not fit the Created/Changed columns", lang, s)
			}
		}
	}
	if s := formatDurationCompact(987*time.Hour + 59*time.Minute); runeLen(s) > width("time") {
		t.Errorf("time %q does not fit the Time column's %d", s, width("time"))
	}
}
