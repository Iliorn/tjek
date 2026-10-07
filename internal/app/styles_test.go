package app

import (
	"fmt"
	"math"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestThemeTextIsLegible holds every theme's text to WCAG's contrast for body
// text, 4.5:1 on the base, and its secondary text to 3:1 on the selection
// background too, so a selected row's dates and scores stay readable. Borders
// and rules (dim) are not text, and are left out.
func TestThemeTextIsLegible(t *testing.T) {
	t.Cleanup(func() { applyTheme(themes[0]) })
	for _, theme := range themes {
		t.Run(theme.name, func(t *testing.T) {
			for name, c := range map[string]lipgloss.Color{"fg": theme.fg, "muted": theme.muted, "help": theme.help} {
				if r := contrast(t, c, theme.bg); r < 4.5 {
					t.Errorf("%s %s on bg %s: %.2f:1, want at least 4.5:1", name, c, theme.bg, r)
				}
			}
			if r := contrast(t, theme.muted, theme.sel); r < 3 {
				t.Errorf("muted %s on sel %s: %.2f:1, want at least 3:1", theme.muted, theme.sel, r)
			}
			if contrast(t, theme.muted, theme.bg) >= contrast(t, theme.fg, theme.bg) {
				t.Errorf("muted %s is as strong as fg %s: secondary text must stay quieter", theme.muted, theme.fg)
			}
			applyTheme(theme)
			if got := dimStyle.GetForeground(); got != theme.muted {
				t.Errorf("dimStyle draws text in %v, want muted %v", got, theme.muted)
			}
			if got := ruleStyle.GetForeground(); got != theme.dim {
				t.Errorf("ruleStyle draws structure in %v, want dim %v", got, theme.dim)
			}
		})
	}
}

// contrast is the WCAG 2 contrast ratio between two #rrggbb colours.
func contrast(t *testing.T, a, b lipgloss.Color) float64 {
	t.Helper()
	lum := func(c lipgloss.Color) float64 {
		var r, g, b int
		if _, err := fmt.Sscanf(string(c), "#%02x%02x%02x", &r, &g, &b); err != nil {
			t.Fatalf("colour %q is not #rrggbb", c)
		}
		ch := func(v int) float64 {
			x := float64(v) / 255
			if x <= 0.03928 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		return 0.2126*ch(r) + 0.7152*ch(g) + 0.0722*ch(b)
	}
	la, lb := lum(a), lum(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

func TestBoardTabUsesDistinctThemeColor(t *testing.T) {
	t.Cleanup(func() { applyTheme(themes[0]) })

	for _, theme := range themes {
		t.Run(theme.name, func(t *testing.T) {
			applyTheme(theme)

			if got := tabBoardActiveStyle.GetBackground(); got != theme.yellow {
				t.Errorf("active Board background = %v, want theme yellow %v", got, theme.yellow)
			}
			if got := tabBoardInactiveStyle.GetForeground(); got != theme.yellow {
				t.Errorf("inactive Board foreground = %v, want theme yellow %v", got, theme.yellow)
			}
			if tabBoardActiveStyle.GetBackground() == tabTagsActiveStyle.GetBackground() {
				t.Error("active Board and Tags tabs must use distinct colors")
			}
			if tabBoardInactiveStyle.GetForeground() == tabTagsInactiveStyle.GetForeground() {
				t.Error("inactive Board and Tags tabs must use distinct colors")
			}
		})
	}
}
