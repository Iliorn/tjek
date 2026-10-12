package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// While an update downloads, the About page shows it as a bar with how far
// it has got, in place of the status line, and the bar goes when it is done.
func TestUpdateDownloadShowsABar(t *testing.T) {
	m := settingsModel(t)
	m.settingsCursor = settingCheckUpdate
	m.updateStatus = "Updating…"
	m.updateDL = updateDownload{active: true, version: "v9.9.9", total: -1}

	next, _, _ := m.handleBackgroundMsg(updateProgressMsg{done: 3 << 20, total: 8 << 20, ch: make(chan updateProgressMsg)})
	m = next.(model)
	page, _ := m.renderSettingsSection(100)
	page = ansi.Strip(page)
	for _, want := range []string{"Downloading v9.9.9", "37%", "3.0 / 8.0 MB", "█"} {
		if !strings.Contains(page, want) {
			t.Errorf("the About page lacks %q while downloading:\n%s", want, page)
		}
	}
	if strings.Contains(page, "Updating…") {
		t.Errorf("the bar should stand in for the status line:\n%s", page)
	}

	next, _, _ = m.handleBackgroundMsg(updateDoneMsg{})
	m = next.(model)
	if page, _ := m.renderSettingsSection(100); strings.Contains(ansi.Strip(page), "Downloading") {
		t.Errorf("the bar outlived the download:\n%s", ansi.Strip(page))
	}
}

// The bar's colours run dark to bright orange along what is filled.
func TestUpdateBarRunsDarkToBrightOrange(t *testing.T) {
	first := updateProgressGradient[0].GetForeground()
	last := updateProgressGradient[len(updateProgressGradient)-1].GetForeground()
	if first == last {
		t.Fatal("the gradient has one colour")
	}
	if bar := renderGradientBar(1, 10, updateProgressGradient); ansi.Strip(bar) != strings.Repeat("█", 10) {
		t.Errorf("a full bar = %q", ansi.Strip(bar))
	}
}
