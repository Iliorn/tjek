package app

import "testing"

// The app opens on the chosen tab, or for "last" on the tab the previous
// session moved to last; a switched-off tab and Settings fall back to Tasks,
// and a restored search stays with Tasks.
func TestStartTab(t *testing.T) {
	setTestHome(t, t.TempDir())
	open := func() model {
		t.Helper()
		return initialModel(&fakeRepo{})
	}

	if m := open(); m.tab != tabTasks {
		t.Fatalf("no setting: opened on %d, want Tasks", m.tab)
	}

	if err := saveSettings(appSettings{StartTab: "board", Search: "#work"}); err != nil {
		t.Fatal(err)
	}
	m := open()
	if m.tab != tabBoard {
		t.Fatalf("start_tab board: opened on %d", m.tab)
	}
	if m.searchQuery != "" || m.tabViews[tabTasks].search != "#work" {
		t.Fatalf("search should stay with Tasks: live %q, Tasks %q", m.searchQuery, m.tabViews[tabTasks].search)
	}
	if m.persistedSearch() != "#work" {
		t.Fatalf("persisted search = %q", m.persistedSearch())
	}

	if err := saveSettings(appSettings{StartTab: "board", BoardDisabled: true}); err != nil {
		t.Fatal(err)
	}
	if m := open(); m.tab != tabTasks {
		t.Fatalf("hidden board: opened on %d, want Tasks", m.tab)
	}

	if err := saveSettings(appSettings{StartTab: "last"}); err != nil {
		t.Fatal(err)
	}
	m = open()
	m.switchTab(tabStats)
	m.switchTab(tabSettings)
	if m := open(); m.tab != tabStats {
		t.Fatalf("last: opened on %d, want Stats (Settings is not remembered)", m.tab)
	}

	m.settingsCursor = settingStartTab
	m.cycleStartTab(1)
	if m.startTab != tabTasks {
		t.Fatalf("after last comes Tasks, got %d", m.startTab)
	}
	m.cycleStartTab(-1)
	m.cycleStartTab(-1)
	if m.startTab != tabStats {
		t.Fatalf("stepping back from last wraps to Stats, got %d", m.startTab)
	}
}
