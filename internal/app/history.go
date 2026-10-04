package app

import (
	"os"
	"os/user"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Iliorn/tjek/todo"
)

// history.go is the app's side of a task's history (todo.Event): who the
// device's edits are signed as. The events themselves are written by the
// save (stamps.go) and drawn by the detail pane (view_history.go).

// authorName is the name this device's edits carry: TJEK_AUTHOR, so a
// script or an agent can sign as itself, then the Settings name, then the
// account's own.
func authorName(s appSettings) string {
	if n := strings.TrimSpace(os.Getenv("TJEK_AUTHOR")); n != "" {
		return n
	}
	if n := strings.TrimSpace(s.Name); n != "" {
		return n
	}
	return accountName()
}

// accountName is the logged-in account's full name, or its login name when
// it has none. A GECOS name carries its office and phone fields after
// commas, and a Windows login its domain before a backslash.
func accountName() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	if full, _, _ := strings.Cut(u.Name, ","); strings.TrimSpace(full) != "" {
		return strings.TrimSpace(full)
	}
	login := u.Username
	if i := strings.LastIndexByte(login, '\\'); i >= 0 {
		login = login[i+1:]
	}
	return login
}

// adoptSaved takes what a save wrote (saveDoneMsg) into the live tasks, so it
// shows without a reload: the history with the event it recorded, the
// authors it gave new comments and time entries, and what the save found
// others had changed in the store. Each live task keeps the edits made since
// the save drained it (sent), applied to the saved version (rebase), which
// becomes its base. The caches are rebuilt only when the save brought in
// something of someone else's.
func (m *model) adoptSaved(saved []*todo.Todo, sent map[string]*todo.Todo) {
	refreshed := false
	for _, s := range saved {
		t, d := m.get(s.ID), sent[s.ID]
		if t == nil || d == nil {
			continue
		}
		next := rebase(s, d, t)
		if next.Deleted {
			// Deleted elsewhere while edited here; the edit did not undo
			// the delete, so the task goes, as a sync would decide.
			m.Store.remove(s.ID)
			delete(m.base, s.ID)
			refreshed = true
			continue
		}
		refreshed = refreshed || !sameContent(&next, t)
		m.Store.replace(next)
		m.Store.setBase(s)
	}
	if refreshed {
		m.markCacheDirty()
	} else if len(saved) > 0 {
		m.invalidateDetailCache()
	}
}

// updateEditName handles the Settings name editor. The field is pre-filled
// with the current name, so clearing it goes back to the account's.
func (m model) updateEditName(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "enter":
			m.userName = strings.TrimSpace(m.textInput.Value())
			m.repo.SetAuthor(authorName(appSettings{Name: m.userName}))
			m.refreshTimerScope()
			m.persistSettings()
			m.mode = modeNormal
			return m, nil
		case "esc":
			m.mode = modeNormal
			return m, nil
		}
	}
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}
