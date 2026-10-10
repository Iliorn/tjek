package app

import "fmt"

// rankmark.go — b on the Tags and Projects tabs marks a tag or a project to
// rank its tasks higher or lower than the rest, by one priority step
// (rank.MarkStep). The marks live in rank.Biases beside the Sequencer knobs,
// so every scorer — the lists, the CLI, the Settings preview — reads them, and
// they are kept in settings.json, this device's own, as the knobs are.

// selectedGroupKey is the tag ("#name") or project ("@name") under the cursor
// on the Tags or Projects list, "" anywhere else (and on "(untagged)").
func (m model) selectedGroupKey() string {
	switch {
	case m.tab == tabTags && !m.tagTaskMode:
		if tags := m.getFilteredTagsForTab(); m.tagTabCursor < len(tags) && tags[m.tagTabCursor] != untaggedKey {
			return "#" + tags[m.tagTabCursor]
		}
	case m.tab == tabProjects && !m.projectTaskMode:
		if projects := m.allProjectsForList(); m.projectCursor < len(projects) {
			return "@" + projects[m.projectCursor]
		}
	}
	return ""
}

// cycleRankMark steps key through as usual → higher → lower → as usual.
// Marks.With builds new maps, as the ranker is copied into the save
// goroutine, which may be reading the old ones.
func (m *model) cycleRankMark(key string) {
	marks := &m.rank.Biases.Marks
	var msg string
	switch marks.Get(key) {
	case 1:
		*marks = marks.With(key, -1)
		msg = tr("%s: its tasks rank lower")
	case -1:
		*marks = marks.With(key, 0)
		msg = tr("%s: its tasks rank as usual")
	default:
		*marks = marks.With(key, 1)
		msg = tr("%s: its tasks rank higher")
	}
	m.markCacheDirty()
	m.persistSettings()
	m.flashInfo(fmt.Sprintf(msg, key))
}

// moveRankMark carries a mark across a rename or a merge, from oldKey to
// newKey. A merge into a group with a mark of its own keeps that one. The
// rename's undo entry keeps the marks as they were (noteUndoMarks), so u
// puts the mark back with the tasks.
func (m *model) moveRankMark(oldKey, newKey string) {
	marks := &m.rank.Biases.Marks
	dir := marks.Get(oldKey)
	if dir == 0 {
		return
	}
	m.noteUndoMarks(*marks)
	*marks = marks.With(oldKey, 0)
	if marks.Get(newKey) == 0 {
		*marks = marks.With(newKey, dir)
	}
	m.persistSettings()
}

// rankMarkSuffix is what a marked row carries after its name: ▲ for higher,
// ▼ for lower.
func (m model) rankMarkSuffix(key string) string {
	switch m.rank.Biases.Marks.Get(key) {
	case 1:
		return " ▲"
	case -1:
		return " ▼"
	}
	return ""
}
