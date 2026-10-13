package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Iliorn/tjek/todo"
)

// recurPickModel is a model with task open in the detail pane, on its
// Recurrence field.
func recurPickModel(t *testing.T, task todo.Todo) model {
	t.Helper()
	m := modelWithTasks(t, task)
	m.pane = paneDetail
	m.detail = detailState{field: fieldRecurrence}
	return m
}

// Enter on Recurrence opens the picker, and every second week or every
// fourth month is a number and a period away.
func TestRecurPickSetsAnInterval(t *testing.T) {
	for _, c := range []struct {
		keys []string
		want string
	}{
		{[]string{"enter", "2", "enter"}, "every:2w"},
		{[]string{"enter", "4", "right", "up", "enter"}, "every:4m"},
		{[]string{"enter", "right", "down", "enter"}, "daily"},
		{[]string{"enter", "1", "2", "right", "up", "up", "enter"}, "every:12y"},
		{[]string{"enter", "up", "up", "enter"}, "every:3w"},
		{[]string{"enter", "9", "backspace", "enter"}, "weekly"},
	} {
		task := todo.New("water the plants")
		m := recurPickModel(t, task)
		m = script(t, m, c.keys...)
		if m.mode != modeNormal {
			t.Errorf("%v: still in the picker", c.keys)
		}
		if got := m.get(task.ID).Recurrence; got != c.want {
			t.Errorf("%v: recurrence = %q, want %q", c.keys, got, c.want)
		}
	}
}

// The picker opens on the rule there is, keeps its weekdays while it stays
// weekly and its end always, and esc leaves the rule alone.
func TestRecurPickKeepsTheRestOfTheRule(t *testing.T) {
	task := todo.New("gym")
	task.SetRecurrence("weekly:mon,thu/until:2027-12-31")
	m := recurPickModel(t, task)

	m = script(t, m, "enter")
	if p := m.recurPick; m.mode != modeRecurPick || p.n != 1 || p.unit != 'w' {
		t.Fatalf("picker opened on %d%c (mode %v), want 1w", p.n, p.unit, m.mode)
	}
	m = script(t, m, "2", "esc")
	if got := m.get(task.ID).Recurrence; got != "weekly:mon,thu/until:2027-12-31" {
		t.Errorf("esc changed the rule to %q", got)
	}
	m = script(t, m, "enter", "2", "enter")
	if got := m.get(task.ID).Recurrence; got != "every:2w:mon,thu/until:2027-12-31" {
		t.Errorf("every 2 weeks: rule = %q, want the weekdays and end kept", got)
	}
	m = script(t, m, "enter", "right", "up", "enter")
	if got := m.get(task.ID).Recurrence; got != "every:2m/until:2027-12-31" {
		t.Errorf("monthly: rule = %q, want the weekdays dropped and the end kept", got)
	}
	m = script(t, m, "enter", "x")
	if m.get(task.ID).IsRecurring() {
		t.Error("x should stop the task repeating")
	}
	m = script(t, m, "u")
	if got := m.get(task.ID).Recurrence; got != "every:2m/until:2027-12-31" {
		t.Errorf("undo brought back %q", got)
	}
}

// The footer shows the interval with the part ↑/↓ change, the date the
// series brings next, and the keys; the field reads it in words.
func TestRecurPickShowsTheIntervalAndTheNextDate(t *testing.T) {
	task := todo.New("pay rent")
	task.DueDate = time.Date(2026, 10, 15, 0, 0, 0, 0, time.Local)
	m := recurPickModel(t, task)
	m.termWidth, m.termHeight = 140, 40
	m = script(t, m, "enter", "2")
	footer := ansi.Strip(m.footerContentFor(130))
	for _, want := range []string{"Repeat every ‹ 2 › ‹ weeks ›", "next Thu 29-10-26", "enter save"} {
		if !strings.Contains(footer, want) {
			t.Errorf("footer lacks %q:\n%s", want, footer)
		}
	}
	m = script(t, m, "enter")
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↻ every 2 weeks") {
		t.Errorf("the Recurrence field should read the rule in words:\n%s", view)
	}
}

// In colour, the picker's line carries its colour codes intact: shortening
// it once cleaned the escape out of each, which printed them as text. And
// the task stays on screen, its detail pane open under the list, while its
// rule is picked.
func TestRecurPickDrawsInColourWithTheDetailOpen(t *testing.T) {
	before := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(before)

	task := todo.New("pay rent")
	m := modelWithTasks(t, task)
	m.termWidth, m.termHeight = 110, 32
	m = sendKey(t, m, "enter")
	m.detail.field = fieldRecurrence
	if m.detailPos != detailBottom {
		t.Skipf("detail position %v; this test is for the stacked pane", m.detailPos)
	}
	m = script(t, m, "enter")
	if m.mode != modeRecurPick {
		t.Fatalf("mode = %v, want the picker", m.mode)
	}
	view := m.View()
	plain := ansi.Strip(view)
	if strings.Contains(plain, "[0m") || strings.Contains(plain, ";2;") {
		t.Errorf("colour codes printed as text:\n%s", plain)
	}
	if !strings.Contains(plain, "Repeat every ‹ 1 › ‹ week ›") {
		t.Errorf("the picker is missing:\n%s", plain)
	}
	if !strings.Contains(plain, "Recurrence:") {
		t.Errorf("the detail pane closed while the picker is open:\n%s", plain)
	}
	if got, want := strings.Count(plain, "\n")+1, m.termHeight; got > want {
		t.Errorf("the view is %d lines in a %d-line window", got, want)
	}
}
