package app

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Iliorn/tjek/todo"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// recurpick.go is the detail pane's recurrence picker: "Repeat every ‹ 2 ›
// ‹ weeks ›" in the footer, the number and the period each set on its own,
// so every second week or every fourth month is two keys away rather than
// a rule to remember. Enter on the Recurrence field used to step through
// five fixed rules, and anything else had to be typed as r:2w.
//
// It sets the interval only. A rule's weekdays and end, which r types, are
// kept when the interval changes, the weekdays as long as it stays weekly.

// recurUnits is the periods the picker steps through, in order.
var recurUnits = []byte{'d', 'w', 'm', 'y'}

// recurPicker is the picker's state while it is open.
type recurPicker struct {
	taskID string
	n      int
	unit   byte
	// onUnit is whether ↑/↓ change the period rather than the number.
	onUnit bool
	// typed is whether a digit has been typed since the number had focus,
	// so the first digit replaces the number and the next ones extend it.
	typed bool
}

// maxRecurN is the largest interval the picker offers, the rule's own cap.
const maxRecurN = 999

// startRecurPick opens the picker on t's rule: its interval when it has one
// the picker can show, else every 1 week.
func (m *model) startRecurPick(t *todo.Todo) {
	p := recurPicker{taskID: t.ID, n: 1, unit: 'w'}
	if r, ok := todo.ParseRule(t.Recurrence); ok && !r.Workdays {
		p.n, p.unit = r.N, r.Unit
	}
	m.recurPick = p
	m.mode = modeRecurPick
}

// startRecurText opens the text prompt for any rule, as r on the field does.
func (m model) startRecurText(t *todo.Todo) (tea.Model, tea.Cmd) {
	m.mode = modeInput
	m.textInput.SetValue(recurInputValue(t.Recurrence))
	m.textInput.CursorEnd()
	m.textInput.Placeholder = tr("Repeat (weekly, mon,thu, 2w:fri, monthly/until:31-12-27, daily/10x)...")
	m.textInput.Focus()
	return m, textinput.Blink
}

// pickedRule is the rule the picker would save on t: its interval, with t's
// weekdays (while weekly) and end kept.
func (p recurPicker) pickedRule(t *todo.Todo) todo.Rule {
	r := todo.Rule{N: p.n, Unit: p.unit}
	if old, ok := todo.ParseRule(t.Recurrence); ok {
		r.Until, r.Count = old.Until, old.Count
		if p.unit == 'w' && old.Unit == 'w' && !old.Workdays {
			r.Days = old.Days
		}
	}
	return r
}

func (m model) updateRecurPick(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	t := m.get(m.recurPick.taskID)
	if t == nil {
		m.mode = modeNormal
		return m, nil
	}
	p := &m.recurPick
	switch k := key.String(); k {
	case "esc":
		m.mode = modeNormal
	case "enter":
		m.mode = modeNormal
		rule := p.pickedRule(t).String()
		if rule != t.Recurrence {
			m.pushUndo("set recurrence", t.ID)
			t.SetRecurrence(rule)
			m.markModified(t.ID)
			m.invalidateDetailCache()
		}
	case "x", "delete":
		m.mode = modeNormal
		if t.IsRecurring() {
			m.pushUndo("clear recurrence", t.ID)
			t.ClearRecurrence()
			m.markModified(t.ID)
			m.invalidateDetailCache()
		}
	case "r":
		return m.startRecurText(t)
	case "left", "right", "tab", "shift+tab", "h", "l":
		p.onUnit, p.typed = !p.onUnit, false
	case "up", "k", "down", "j":
		dir := 1
		if k == "down" || k == "j" {
			dir = -1
		}
		if p.onUnit {
			p.unit = stepIn(recurUnits, p.unit, dir)
		} else {
			p.n = min(max(p.n+dir, 1), maxRecurN)
		}
		p.typed = false
	case "backspace":
		p.onUnit = false
		p.n = max(p.n/10, 1)
		p.typed = true
	default:
		if len(k) == 1 && k[0] >= '0' && k[0] <= '9' {
			d := int(k[0] - '0')
			n := d
			if p.typed && !p.onUnit {
				n = p.n*10 + d
			}
			if n >= 1 && n <= maxRecurN {
				p.n = n
			}
			p.onUnit, p.typed = false, true
		}
	}
	return m, nil
}

// recurUnitWord is the period as the picker shows it, agreeing with n.
func recurUnitWord(unit byte, n int) string {
	switch unit {
	case 'd':
		if n == 1 {
			return tr("day")
		}
		return tr("days")
	case 'm':
		if n == 1 {
			return tr("month")
		}
		return tr("months")
	case 'y':
		if n == 1 {
			return tr("year")
		}
		return tr("years")
	}
	if n == 1 {
		return tr("week")
	}
	return tr("weeks")
}

// renderRecurPick is the picker's two footer lines: the interval with the
// part ↑/↓ change lit and the date the series would bring next, then its keys.
func (m model) renderRecurPick(w int) string {
	p := m.recurPick
	part := func(s string, lit bool) string {
		if lit {
			return selectedStyle.Render("‹ " + s + " ›")
		}
		return normalStyle.Render("‹ " + s + " ›")
	}
	line := calTodayStyle.Render("    "+tr("Repeat every")+" ") +
		part(strconv.Itoa(p.n), !p.onUnit) + " " + part(recurUnitWord(p.unit, p.n), p.onUnit)
	if t := m.get(p.taskID); t != nil {
		if next, ok := recurPreviewNext(p.pickedRule(t), t, m.frameTime); ok {
			line += helpStyle.Render(fmt.Sprintf("  · "+tr("next %s"), localizedWeekdayShort(next.Weekday())+" "+next.Format("02-01-06")))
		}
	}
	hints := helpStyle.Render("    " + truncate(tr("↑/↓ or 0-9 change · ←/→ switch · enter save · x no repeat · r write a rule · esc cancel"), max(w-4, 8)))
	return truncate(line, w) + "\n" + hints
}

// recurPreviewNext is the instance r brings after t's due date, or after
// today for a task with none: the date closing t would give the next one.
func recurPreviewNext(r todo.Rule, t *todo.Todo, now time.Time) (time.Time, bool) {
	from := t.DueDate
	if from.IsZero() {
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}
	next, _, ok := r.Next(from, from, 0)
	return next, ok
}

// recurIntervalText is a rule's interval in words: "every 2 weeks".
func recurIntervalText(r todo.Rule) string {
	if r.N == 1 || r.Workdays {
		return tr(r.Interval())
	}
	switch r.Unit {
	case 'd':
		return fmt.Sprintf(tr("every %d days"), r.N)
	case 'm':
		return fmt.Sprintf(tr("every %d months"), r.N)
	case 'y':
		return fmt.Sprintf(tr("every %d years"), r.N)
	}
	return fmt.Sprintf(tr("every %d weeks"), r.N)
}
