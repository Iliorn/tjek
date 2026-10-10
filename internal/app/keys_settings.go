package app

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// keys_settings.go — the Settings page Keys: one row per rebindable action,
// enter to press a new key, backspace to put the default back. It writes the
// same overrides settings.json "keys" holds (keys.go), so a rebind made here
// and one made by hand are the same thing.

// keyPageActions are the Keys page's rows, in the order the help lists them.
// Every single-key action is here except the ones on enter and esc: those
// keys mean "do the obvious thing" and "go back" in every context, and moving
// one in one place would break that everywhere else.
var keyPageActions = []string{
	"add", "done", "track", "timeentry", "priority", "setdue", "edit",
	"delete", "notes", "focus", "context", "why", "sort", "history", "search",
	"quicktag", "quickproject", "tagfilter", "merge", "share",
	"help", "undo", "quit",
}

// keyActionLabels names each action on the Keys page, in English.
var keyActionLabels = map[string]string{
	"add":          "Add",
	"done":         "Done",
	"track":        "Time tracking",
	"timeentry":    "Manual time entry",
	"priority":     "Priority",
	"setdue":       "Due date",
	"edit":         "Rename / edit",
	"delete":       "Delete",
	"notes":        "Description",
	"focus":        "Focus",
	"context":      "Context",
	"why":          "Why this rank",
	"sort":         "Sort",
	"history":      "Show finished",
	"search":       "Filter",
	"quicktag":     "Quick tag",
	"quickproject": "Quick project",
	"tagfilter":    "Show on the Tasks tab",
	"merge":        "Merge tags",
	"share":        "Share project",
	"help":         "Help",
	"undo":         "Undo",
	"quit":         "Quit",
}

// settingKeyAction is the action a Settings row of the Keys page rebinds.
func settingKeyAction(id int) (string, bool) {
	if id < settingKeyFirst || id >= settingKeyFirst+len(keyPageActions) {
		return "", false
	}
	return keyPageActions[id-settingKeyFirst], true
}

// settingKeyRows are the Keys page's rows.
func settingKeyRows() []int {
	rows := make([]int, len(keyPageActions))
	for i := range rows {
		rows[i] = settingKeyFirst + i
	}
	return rows
}

// keyRowValue is what a Keys row shows: the key, and the default beside it
// once the key has been moved.
func keyRowValue(action string) string {
	def := rebindableActions()[action]
	key := effectiveKey(action, def)
	if key == def {
		return key
	}
	return key + "  (" + tr("default") + " " + def + ")"
}

// keyActionContexts is every context the action is bound in.
func keyActionContexts(action string) keyCtx {
	var ctx keyCtx
	for i := range keymap {
		if keymap[i].action == action {
			ctx |= keymap[i].ctx
		}
	}
	return ctx
}

// keyTokens splits a registry key's display form into the keys it stands
// for: "←/→ ↑/↓" is four arrows, "tab / shift+tab / 1-7" is nine keys.
// A single-key binding is its own one token.
func keyTokens(display string) []string {
	if paletteSendable(display) {
		return []string{display}
	}
	f := strings.FieldsFunc(display, func(r rune) bool { return r == ' ' || r == '/' || r == '·' })
	var out []string
	for _, t := range f {
		switch t {
		case "←":
			out = append(out, "left")
		case "→":
			out = append(out, "right")
		case "↑":
			out = append(out, "up")
		case "↓":
			out = append(out, "down")
		case "space":
			out = append(out, " ")
		case "1-7":
			for d := '1'; d <= '7'; d++ {
				out = append(out, string(d))
			}
		default:
			out = append(out, t)
		}
	}
	return out
}

// keyRebindProblem says why key cannot become action's key, or "" when it
// can. It refuses anything but one printable character, and a key that
// already does something else in a place where the action is live: two
// actions on one key would leave one of them unreachable.
func keyRebindProblem(action, key string) string {
	if key == " " || len([]rune(key)) != 1 {
		return tr("Only a single letter, digit or sign can be used")
	}
	ctx := keyActionContexts(action)
	for i := range keymap {
		bd := &keymap[i]
		if bd.ctx&ctx == 0 || bd.action == action {
			continue
		}
		taken := false
		if paletteSendable(bd.key) {
			taken = effectiveKey(bd.action, bd.key) == key
		} else {
			for _, t := range keyTokens(bd.key) {
				taken = taken || t == key
			}
		}
		if taken {
			name := tr(bd.desc)
			if l, ok := keyActionLabels[bd.action]; ok {
				name = tr(l)
			}
			return fmt.Sprintf(tr("%s is already used for: %s"), key, name)
		}
	}
	return ""
}

// startKeyCapture opens the "press the new key" prompt for a Keys row.
func (m *model) startKeyCapture(action string) {
	m.mode = modeCaptureKey
	m.captureAction = action
	m.confirmMsg = fmt.Sprintf(tr("Press the new key for %s · esc cancels"), tr(keyActionLabels[action]))
}

// updateCaptureKey takes the next key press as the action's new key.
func (m model) updateCaptureKey(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	action := m.captureAction
	m.mode = modeNormal
	m.captureAction = ""
	if key.String() == "esc" {
		return m, nil
	}
	if p := keyRebindProblem(action, key.String()); p != "" {
		m.flashError(p)
		return m, clearErrAfter()
	}
	m.setKeyOverride(action, key.String())
	return m, nil
}

// setKeyOverride puts action on key (the default key clears the override),
// applies it and saves it. The sanitizer runs over the result as it does at
// startup, so the page can never store a map the next launch would reject.
func (m *model) setKeyOverride(action, key string) {
	next := make(map[string]string, len(activeKeys)+1)
	for a, k := range activeKeys {
		next[a] = k
	}
	if key == rebindableActions()[action] {
		delete(next, action)
	} else {
		next[action] = key
	}
	clean, _ := sanitizeKeyOverrides(next)
	applyKeys(clean)
	m.persistSettings()
}
