package app

import "strings"

// startTabLast is the "Start on" choice that reopens the tab the last session
// was on, rather than a fixed one.
const startTabLast tab = -1

// startTabWords are the settings.json spellings of the tabs the app can open
// on. Settings is not one: a session that ends there was usually changing a
// setting, and opening on it again would hide the task lists behind it.
var startTabWords = map[tab]string{
	startTabLast: "last",
	tabTasks:     "tasks",
	tabCalendar:  "calendar",
	tabTags:      "tags",
	tabProjects:  "projects",
	tabBoard:     "board",
	tabStats:     "stats",
}

// startTabOrder is the order the Settings row steps through.
var startTabOrder = []tab{startTabLast, tabTasks, tabCalendar, tabTags, tabProjects, tabBoard, tabStats}

func startTabString(t tab) string {
	if w, ok := startTabWords[t]; ok {
		return w
	}
	return startTabWords[tabTasks]
}

// startTabFromSettings reads a stored word; anything else, absent included,
// is Tasks, so a typo costs the setting and not the start.
func startTabFromSettings(s string) tab {
	s = strings.ToLower(strings.TrimSpace(s))
	for t, w := range startTabWords {
		if w == s {
			return t
		}
	}
	return tabTasks
}

// openingTab is the tab a session starts on: the chosen one, or the last one
// for "last", falling back to Tasks when that tab is switched off.
func openingTab(choice, last tab, c boardConfig) tab {
	t := choice
	if t == startTabLast {
		t = last
	}
	if t == startTabLast || t == tabSettings || !c.tabVisible(t) {
		return tabTasks
	}
	return t
}

// trStartTab is the choice as the Settings row says it: a tab by its name on
// the tab bar.
func trStartTab(t tab) string {
	if t == startTabLast {
		return tr("Last used")
	}
	return paletteTabName(t)
}

// cycleStartTab steps the "Start on" choice by dir, skipping tabs that are
// switched off.
func (m *model) cycleStartTab(dir int) {
	n := len(startTabOrder)
	i := 0
	for j, t := range startTabOrder {
		if t == m.startTab {
			i = j
		}
	}
	step := 1
	if dir < 0 {
		step = n - 1
	}
	for range n {
		i = (i + step) % n
		if t := startTabOrder[i]; t == startTabLast || m.boardCfg.tabVisible(t) {
			m.startTab = t
			break
		}
	}
	m.persistSettings()
}
