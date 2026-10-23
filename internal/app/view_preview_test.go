package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestQuickAddPreviewParsedFields(t *testing.T) {
	applyLang(string(langEN))
	got := ansi.Strip(renderQuickAddPreview("Buy milk #shopping due:tomorrow p:high s:l", 0, 120))
	for _, want := range []string{`"Buy milk"`, "#shopping", "due ", "p:high", "s:L"} {
		if !strings.Contains(got, want) {
			t.Errorf("preview %q missing %q", got, want)
		}
	}
}

func TestQuickAddPreviewUsesCanonicalTagSlug(t *testing.T) {
	applyLang(string(langEN))
	got := ansi.Strip(renderQuickAddPreview("Plan work #Deep-Work", 0, 120))
	if !strings.Contains(got, "#deep-work") {
		t.Errorf("preview should show the canonical stored tag; got %q", got)
	}
	if strings.Contains(got, "#Deep-Work") {
		t.Errorf("preview should not show a spelling AddTag will later change; got %q", got)
	}
}

// A mistyped token must not become a chip — it stays inside the title quotes,
// which is exactly the typo-catching signal the preview exists for.
func TestQuickAddPreviewKeepsBadTokensInTitle(t *testing.T) {
	applyLang(string(langEN))
	got := ansi.Strip(renderQuickAddPreview("Buy milk due:tomorow p:hgih", 0, 120))
	if !strings.Contains(got, `"Buy milk due:tomorow p:hgih"`) {
		t.Errorf("bad tokens should stay in the title; got %q", got)
	}
	if strings.Contains(got, "due 0") || strings.Contains(got, "p:high") {
		t.Errorf("no field chip should render for mistyped tokens; got %q", got)
	}
}

func TestSearchPreviewFiltersAndTitle(t *testing.T) {
	applyLang(string(langEN))
	got := ansi.Strip(renderSearchPreview("#shop @home p:high due:<friday milk", 120))
	for _, want := range []string{"#shop", "@home", "p:high", "due <", `title~ "milk"`} {
		if !strings.Contains(got, want) {
			t.Errorf("search preview %q missing %q", got, want)
		}
	}
}

// A mistyped due: filter falls into the fuzzy title-match run rather than
// vanishing silently.
func TestSearchPreviewBadDueFallsToTitle(t *testing.T) {
	applyLang(string(langEN))
	got := ansi.Strip(renderSearchPreview("due:tomorow overdue grcry", 120))
	if !strings.Contains(got, "overdue") {
		t.Errorf("recognised 'overdue' should be a chip; got %q", got)
	}
	if !strings.Contains(got, `title~ "due:tomorow grcry"`) {
		t.Errorf("mistyped due token should land in the title run; got %q", got)
	}
}

// Both previews honour the width budget (no-wrap contract).
func TestPreviewsRespectWidth(t *testing.T) {
	applyLang(string(langEN))
	long := "A very long task title here #alpha #beta #gamma @someproject due:tomorrow p:high s:large r:weekly dep:^"
	for _, w := range []int{20, 40, 80} {
		if got := ansi.StringWidth(renderQuickAddPreview(long, 0, w)); got > w {
			t.Errorf("quick-add preview width %d exceeds budget %d", got, w)
		}
		if got := ansi.StringWidth(renderSearchPreview(long, w)); got > w {
			t.Errorf("search preview width %d exceeds budget %d", got, w)
		}
	}
}

// A token with a field prefix whose value did not parse is named in the
// preview, and by the add itself, instead of disappearing into the title.
func TestQuickAddNamesTheTokensItDidNotUnderstand(t *testing.T) {
	applyLang(string(langEN))
	p := parseQuickAdd("Fix tap due:blah p:hi s:xl #home")
	if got := strings.Join(p.unparsed, " "); got != "due:blah p:hi s:xl" {
		t.Errorf("unparsed = %q, want the three mistyped tokens", got)
	}
	if p := parseQuickAdd("Fix tap due:friday p:h s:l r:weekly"); len(p.unparsed) != 0 {
		t.Errorf("valid tokens reported as not understood: %v", p.unparsed)
	}
	if got := ansi.Strip(renderQuickAddPreview("Fix tap due:blah ", 17, 120)); !strings.Contains(got, "not understood: due:blah") {
		t.Errorf("the preview should name the token: %q", got)
	}
	m := modelWithTasks(t)
	m = sendKey(t, m, "a")
	m.textInput.SetValue("Fix tap due:blah")
	m = sendKey(t, m, "enter")
	if !strings.Contains(m.err, "due:blah") {
		t.Errorf("adding should say the token was kept in the title, flash = %q", m.err)
	}
}

// The word the caret is in is still being typed: due:toda on its way to
// due:today is not named as not understood until the caret leaves it.
func TestQuickAddPreviewWaitsForTheWordBeingTyped(t *testing.T) {
	applyLang(string(langEN))
	preview := func(in string, pos int) string { return ansi.Strip(renderQuickAddPreview(in, pos, 120)) }
	typing := "Mail from Kile due:toda"
	if got := preview(typing, len(typing)); strings.Contains(got, "not understood") {
		t.Errorf("the word being typed was called out: %q", got)
	}
	if got := preview(typing, len(typing)-2); strings.Contains(got, "not understood") {
		t.Errorf("a word being edited in the middle was called out: %q", got)
	}
	if got := preview(typing+" ", len(typing)+1); !strings.Contains(got, "not understood: due:toda") {
		t.Errorf("a finished word should be named once the caret leaves it: %q", got)
	}
	if got := preview("Fix due:blah tap", 16); !strings.Contains(got, "not understood: due:blah") {
		t.Errorf("a bad word behind the caret should be named: %q", got)
	}
	if got := preview("Mail due:today", 14); !strings.Contains(got, "due ") || strings.Contains(got, "not understood") {
		t.Errorf("the finished token should read as a date: %q", got)
	}
}
