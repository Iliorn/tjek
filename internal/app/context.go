package app

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Iliorn/tjek/todo"
)

// context.go — saved filters that stay on, Taskwarrior's contexts. A context
// is a name for a / filter. While one is active, the tasks it does not match
// leave the Tasks list, the board, Stats and the Tags and Projects tabs, under
// whatever / holds, the way the waiting tasks do (cache.outside, listHidden).
// They are this device's own, kept in settings.json like Taskwarrior keeps
// them in .taskrc, and the command line ignores them, so a script never
// misses tasks over a choice made in the app.
//
// F opens the picker: enter switches to a context, a new name saves the
// current / filter under it, del deletes one, and "no context" switches off.

// savedContext is one context: its name, and the filter it stands for.
type savedContext struct {
	Name  string `json:"name"`
	Query string `json:"query"`
}

// contextRowKind is what enter does on a picker row.
type contextRowKind int

const (
	contextRowOff contextRowKind = iota
	contextRowUse
	contextRowSave
	// contextRowNeedFilter explains why a new name cannot be saved yet.
	contextRowNeedFilter
)

type contextRow struct {
	kind  contextRowKind
	name  string
	query string
}

// activeContextQuery is the filter of the active context, "" when none is on
// (or the one named has been deleted on another path).
func (m model) activeContextQuery() string {
	for _, c := range m.contexts {
		if c.Name == m.context {
			return c.Query
		}
	}
	return ""
}

// contextRows are the picker's rows for what has been typed: "no context"
// while one is on, the contexts whose name contains the text, and, for a new
// name, the row that saves the current / filter under it.
func (m model) contextRows() []contextRow {
	typed := strings.TrimSpace(m.ctxPick.query)
	q := strings.ToLower(typed)
	var rows []contextRow
	if m.context != "" && typed == "" {
		rows = append(rows, contextRow{kind: contextRowOff})
	}
	exact := false
	for _, c := range m.contexts {
		if strings.Contains(strings.ToLower(c.Name), q) {
			rows = append(rows, contextRow{kind: contextRowUse, name: c.Name, query: c.Query})
		}
		exact = exact || strings.EqualFold(c.Name, typed)
	}
	if typed != "" && !exact {
		if search := strings.TrimSpace(m.searchQuery); search != "" && search != untaggedKey {
			rows = append(rows, contextRow{kind: contextRowSave, name: typed, query: search})
		} else {
			rows = append(rows, contextRow{kind: contextRowNeedFilter, name: typed})
		}
	}
	return rows
}

func (m *model) openContextPicker() tea.Cmd {
	m.mode = modePickContext
	m.ctxPick = searchState{}
	m.ctxInput.SetValue("")
	return m.ctxInput.Focus()
}

// setContext switches to the named context, "" for none, and keeps the choice
// across restarts.
func (m *model) setContext(name string) {
	m.context = name
	m.cursor, m.listOffset = 0, 0
	m.markCacheDirty()
	m.persistSettings()
}

func (m model) updateContextPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if key, ok := msg.(tea.KeyMsg); ok {
		rows := m.contextRows()
		switch key.String() {
		case "esc":
			m.mode = modeNormal
			return m, nil
		case "up":
			if m.ctxPick.cursor > 0 {
				m.ctxPick.cursor--
			}
			return m, nil
		case "down":
			if m.ctxPick.cursor < len(rows)-1 {
				m.ctxPick.cursor++
			}
			return m, nil
		case "enter":
			if m.ctxPick.cursor >= len(rows) {
				return m, nil
			}
			row := rows[m.ctxPick.cursor]
			switch row.kind {
			case contextRowOff:
				m.setContext("")
			case contextRowUse:
				m.setContext(row.name)
			case contextRowSave:
				m.contexts = append(m.contexts, savedContext{Name: row.name, Query: row.query})
				// The filter now lives in the context; left in / as well, it
				// would narrow the list a second time.
				m.searchQuery = ""
				m.searchInput.SetValue("")
				m.setContext(row.name)
			case contextRowNeedFilter:
				m.flashInfo(tr("Filter with / first, then save the filter here"))
				return m, clearErrAfter()
			}
			m.mode = modeNormal
			return m, nil
		case "delete":
			if m.ctxPick.cursor < len(rows) && rows[m.ctxPick.cursor].kind == contextRowUse {
				name := rows[m.ctxPick.cursor].name
				m.contexts = slices.DeleteFunc(m.contexts, func(c savedContext) bool { return c.Name == name })
				if m.context == name {
					m.setContext("")
				} else {
					m.persistSettings()
				}
				if n := len(m.contextRows()); m.ctxPick.cursor >= n && n > 0 {
					m.ctxPick.cursor = n - 1
				}
			}
			return m, nil
		}
	}
	old := m.ctxPick.query
	m.ctxInput, cmd = m.ctxInput.Update(msg)
	m.ctxPick.query = m.ctxInput.Value()
	if m.ctxPick.query != old {
		m.ctxPick.cursor = 0
	}
	return m, cmd
}

// renderContextPicker draws the picker under the panes: the name field, then
// one row per contextRow in the slots the layout reserves for it.
func (m model) renderContextPicker(w int) string {
	b := getBuilder()
	defer putBuilder(b)
	b.WriteString(searchStyle.Width(w).Render(m.ctxInput.View()))
	rows := m.contextRows()
	start, hasAbove, hasBelow := pickerWindowStart(m.ctxPick.cursor, len(rows), maxContextRows)
	for slot := 0; slot < maxContextRows; slot++ {
		idx := start + slot
		b.WriteString("\n")
		switch {
		case hasAbove && slot == 0:
			b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more above", start+1)))
		case hasBelow && slot == maxContextRows-1:
			b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more below", len(rows)-idx)))
		case idx < len(rows):
			b.WriteString(m.renderContextRow(rows[idx], idx == m.ctxPick.cursor))
		case idx == 0:
			b.WriteString(dimStyle.Render("    " + tr("No contexts yet: filter with /, then type a name here to save it")))
		}
	}
	b.WriteString("\n" + helpStyle.Render("    "+tr("enter use · del delete · esc close")))
	return b.String()
}

func (m model) renderContextRow(r contextRow, selected bool) string {
	gap := "    "
	style := normalStyle
	if selected {
		gap, style = cursorGap+cursorMark, selectedStyle
	}
	switch r.kind {
	case contextRowOff:
		return style.Render(gap + tr("no context: show every task"))
	case contextRowSave:
		return dimStyle.Render(gap+tr("save the filter as: ")) + style.Render(r.name) + dimStyle.Render("  /"+r.query)
	case contextRowNeedFilter:
		return dimStyle.Render(gap + fmt.Sprintf(tr("%s is new: filter with / first to save it"), r.name))
	}
	mark := "  "
	if r.name == m.context {
		mark = "◉ "
	}
	return style.Render(gap+mark+r.name) + dimStyle.Render("  /"+r.query)
}

// outsideContext is the tasks the active context does not match, nil when
// none is on. Its filter is compiled once per refresh, with the same caches
// the / filter reads.
func (m *model) outsideContext(all []*todo.Todo) map[string]bool {
	q := m.activeContextQuery()
	if q == "" {
		return nil
	}
	match := compileSearchWith(q, m.searchEnv())
	out := make(map[string]bool)
	for _, t := range all {
		if !match(*t) {
			out[t.ID] = true
		}
	}
	return out
}
