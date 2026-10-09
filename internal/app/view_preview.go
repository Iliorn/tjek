package app

import (
	"fmt"
	"strings"

	"github.com/Iliorn/tjek/todo"
	"github.com/charmbracelet/x/ansi"
)

// view_preview.go renders the live parse preview shown under the quick-add and
// search inputs. Both inputs use the same token vocabulary but silently drop a
// mistyped token (due:tomorow, p:hgih) into the free text — the title for
// quick-add, the fuzzy title match for search. Echoing the parsed result as the
// user types makes that visible: a token that didn't take shows up in the
// quoted free-text run instead of as its own chip.

// renderQuickAddPreview shows what parseQuickAdd extracts from the current
// quick-add input: the title in quotes, then a chip per recognised field.
// Anything that failed to parse stays inside the title quotes, which is the
// tell that a token was mistyped.
func renderQuickAddPreview(input string, w int) string {
	p := parseQuickAdd(input)

	title := strings.TrimSpace(p.title)
	if title == "" {
		title = tr("(no title yet)")
	}
	parts := []string{selectedStyle.Render(`"` + title + `"`)}
	// Right after the title, so a narrow footer clips the chips before it
	// clips the one thing that says a token did not take.
	if len(p.unparsed) > 0 {
		parts = append(parts, overdueStyle.Render(fmt.Sprintf(tr("not understood: %s"), strings.Join(p.unparsed, " "))))
	}
	for _, tag := range p.tags {
		parts = append(parts, tagStyle.Render("#"+tag))
	}
	if p.project != "" {
		parts = append(parts, projLabelStyle.Render("@"+p.project))
	}
	if !p.dueDate.IsZero() {
		parts = append(parts, normalStyle.Render(tr("due ")+p.dueDate.Format("02-01")))
	}
	if !p.startDate.IsZero() {
		parts = append(parts, normalStyle.Render(fmt.Sprintf(tr("hidden until %s"), startDayText(p.startDate))))
	}
	if p.priority != todo.PriorityMedium {
		parts = append(parts, normalStyle.Render("p:"+trPriority(p.priority)))
	}
	if p.size != todo.SizeMedium {
		parts = append(parts, normalStyle.Render("s:"+p.size.Letter()))
	}
	if p.recurrence != "" {
		parts = append(parts, normalStyle.Render("⟳"+trRecurrence(p.recurrence)))
	}
	if len(p.deps) > 0 {
		parts = append(parts, normalStyle.Render("dep:"+strings.Join(p.deps, ",")))
	}

	line := helpStyle.Render("    → ") + strings.Join(parts, "  ")
	return ansi.Truncate(line, w, "…")
}

// renderQuickAddSuggestions renders the completion row under the quick-add
// field: the candidate chips with the highlighted one selected, and the keys
// that drive them. Deliberately one line — it takes the row the parse preview
// would have used, so the footer never grows and the layout math around it is
// untouched.
func renderQuickAddSuggestions(sigil string, matches []string, sel, w int) string {
	chips := make([]string, 0, len(matches))
	for i, name := range matches {
		label := sigil + name
		switch {
		case i == sel:
			chips = append(chips, selectedStyle.Render(label))
		case sigil == "#":
			chips = append(chips, tagStyle.Render(label))
		default:
			chips = append(chips, projLabelStyle.Render(label))
		}
	}
	line := helpStyle.Render("    ") + strings.Join(chips, "  ") +
		helpStyle.Render("   "+tr("tab insert · ↑/↓ pick"))
	return ansi.Truncate(line, w, "…")
}

// renderSearchPreview shows the filter as parseFilter reads it, while the
// user types: each term a chip, negated ones as "not …", the groups joined by
// "or", and leftover words (a mistyped p:/due: included) as the loose
// title match.
func renderSearchPreview(query string, w int) string {
	var parts []string
	for i, group := range parseFilter(query, filterEnv{}) {
		if i > 0 {
			parts = append(parts, dimStyle.Render(tr("or")))
		}
		for _, term := range group {
			label, style := term.label, normalStyle
			switch term.kind {
			case termTag:
				style = tagStyle
			case termProject:
				style = projLabelStyle
			case termText:
				label, style = tr("title~")+` "`+label+`"`, dimStyle
			case termWord:
				if label == tr("overdue") {
					style = overdueStyle
				}
			}
			if term.neg {
				label = fmt.Sprintf(tr("not %s"), label)
			}
			parts = append(parts, style.Render(label))
		}
	}

	line := helpStyle.Render("    → ") + strings.Join(parts, "  ")
	return ansi.Truncate(line, w, "…")
}

// describeDueFilter renders a due: comparison token for the search preview
// (e.g. "<tomorrow" → "due <05-07"), reporting false for an unparseable date so
// the caller falls the token back into the title-match run. Mirrors the op
// parsing in parseDueFilter.
func describeDueFilter(spec string) (string, bool) {
	op, rest := "", spec
	switch {
	case strings.HasPrefix(spec, "<="):
		op, rest = "≤", spec[2:]
	case strings.HasPrefix(spec, ">="):
		op, rest = "≥", spec[2:]
	case strings.HasPrefix(spec, "<"):
		op, rest = "<", spec[1:]
	case strings.HasPrefix(spec, ">"):
		op, rest = ">", spec[1:]
	}
	d, err := parseDueDate(rest)
	if err != nil {
		return "", false
	}
	return tr("due ") + op + d.Format("02-01"), true
}
