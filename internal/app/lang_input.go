package app

import (
	"fmt"
	"strings"
	"time"
)

// lang_input.go closes the gap the translations left open. The interface spoke
// Danish or German while the parser only understood English, so a fully
// translated screen still asked its user to type `due:friday p:high` — correct
// enough technically, and schizophrenic to read. Every keyword the quick-add
// and search grammars accept now also answers to the active language's
// spelling of it.
//
// The spellings are not a second list: they are read out of the same
// translation table the UI renders from, so the word on screen and the word the
// parser takes cannot drift apart. `tr("high")` is already "høj" because the
// priority column says "Høj"; that is now also what `p:høj` matches.
//
// Three rules keep this from turning into a second grammar:
//
//   - English always parses, in every language. Existing scripts, the README,
//     the man page, and anything the user's fingers already know keep working.
//   - Only *input* is localized. Stored data, the sync wire format and the
//     canonical recurrence strings stay English, so a Danish install and a
//     German one hold the same bytes and sync without translation.
//   - The one-letter field prefixes (p:, s:, r:) stay as they are — they are
//     the initial in all three languages. Only the word-form prefixes get a
//     localized alias.
//
// The CLI is deliberately left out: its help, flags and output are English, so
// its input is too. applyLang is only called when the TUI starts (and when the
// language is switched), which leaves activeInputWords empty for a CLI process
// — the English-only behaviour it had before. Localizing input on a surface
// that answers in English would recreate the mismatch this file exists to
// remove, one layer down.

// inputKeywords are the parser words carrying a localized spelling. Each is the
// canonical English form the grammar switches on, and the key its translation
// is stored under. Adding a keyword here is all it takes to make the active
// language's word for it parse — TestEveryKeywordParsesInEveryLanguage walks
// this list.
var inputKeywords = []string{
	// Date words (parseDueDate).
	"today", "tomorrow", "yesterday", "next week", "next month", "next",
	// Spelled-out periods (periodPhrases).
	"start of week", "end of week", "end of work week", "start of next week",
	"start of month", "end of month", "start of next month",
	"start of quarter", "end of quarter", "start of next quarter",
	"start of year", "end of year", "start of next year",
	// A start date with no day (parseStartDate).
	"someday",
	// Priority values (p:) — "medium" is shared with the size scale.
	"high", "medium", "low",
	// Size values (s:, size:).
	"small", "large",
	// Recurrence rules (r:, recur:).
	"daily", "weekdays", "weekly", "monthly", "yearly",
	// Bare search keywords.
	"overdue", "waiting",
	// Field prefixes whose English form is a word rather than an initial.
	"due:", "size:", "recur:", "dep:", "wait:",
	// A recurrence rule's end (r:weekly/until:…).
	"until:",
}

// extraInputAliases are spellings accepted but never advertised: the
// inflections a language makes of an advertised word, and the transliterations
// of characters that are awkward to type. The derived index already covers the
// space-free variant of every multi-word translation, so only genuine
// irregularities belong here.
var extraInputAliases = map[language]map[string]string{
	langDA: {
		// The bookkeeping words for a month's first and last day.
		"primo":  "start of month",
		"ultimo": "end of month",
	},
	langDE: {
		// German inflects the article-like "nächste" by case and gender;
		// whichever one the user reaches for should find the weekday.
		"nächsten": "next",
		"nächster": "next",
		"nächstes": "next",
		"gross":    "large",
		"groesse:": "size:",
		"faellig:": "due:",
		// "später" is shown for someday; this is the other word for it.
		"irgendwann": "someday",
	},
}

// activeInputWords maps a localized word to the English keyword the grammar
// switches on. It is a package-level global rebuilt by applyLang, following the
// applyTheme / applyLang pattern — the parsers read it directly rather than
// being handed a vocabulary. English boots with an empty map, which is exactly
// right: with no aliases, only English parses.
var activeInputWords = map[string]string{}

// buildInputIndex derives the alias index for a language from the translation
// table plus the calendar's weekday names, and adds the space-free form of
// every multi-word spelling — "i dag" reads correctly in a sentence but cannot
// survive the whitespace tokenisation inside `due:`, so `idag` answers too.
func buildInputIndex(l language) map[string]string {
	idx := map[string]string{}
	if l == langEN {
		return idx
	}
	table := translations[l]
	add := func(alias, canonical string) {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias == "" || alias == canonical {
			return
		}
		idx[alias] = canonical
		if squashed := strings.ReplaceAll(alias, " ", ""); squashed != alias {
			idx[squashed] = canonical
		}
	}
	for _, word := range inputKeywords {
		if local, ok := table[word]; ok {
			add(local, word)
		}
	}
	// Weekdays come from the tables the calendar already renders from, so
	// `due:fredag` works the moment the month grid says "Fredag".
	if names, ok := weekdayNames[l]; ok {
		for i, name := range names {
			add(name, strings.ToLower(time.Weekday(i).String()))
		}
	}
	if abbrevs, ok := weekdayAbbrevs[l]; ok {
		for i, name := range abbrevs {
			add(name, strings.ToLower(time.Weekday(i).String()))
		}
	}
	for alias, canonical := range extraInputAliases[l] {
		add(alias, canonical)
	}
	return idx
}

// canonicalInputWord maps a word the user typed to the English keyword the
// grammar switches on. English input, and anything unrecognised, comes back
// unchanged — the caller's own switch decides whether it means anything. The
// argument is expected already lower-cased, as every parser here lowers first.
func canonicalInputWord(s string) string {
	if canonical, ok := activeInputWords[s]; ok {
		return canonical
	}
	return s
}

// canonicalInputToken rewrites a localized field prefix on a whole token —
// "frist:imorgen" → "due:imorgen" — so the grammar below it only ever sees the
// English prefixes. The value is left alone; the value lookups handle it.
func canonicalInputToken(tok string) string {
	i := strings.Index(tok, ":")
	if i < 0 {
		return tok
	}
	if canonical, ok := activeInputWords[tok[:i+1]]; ok {
		return canonical + tok[i+1:]
	}
	return tok
}

// splitNextPrefix reports the weekday part of a "next <weekday>" phrase in any
// shipped language ("next friday", "næste fredag", "nächsten Freitag").
func splitNextPrefix(s string) (string, bool) {
	first, rest, ok := strings.Cut(s, " ")
	if !ok || rest == "" {
		return "", false
	}
	if canonicalInputWord(first) != "next" {
		return "", false
	}
	return rest, true
}

// inputWord is the active language's spelling of a keyword — the same table the
// parser indexes, read the other way, so the help and the hints advertise
// exactly what will parse.
func inputWord(english string) string { return tr(english) }

// inputToken is inputWord with the spaces taken out, for the places a keyword
// has to survive whitespace tokenisation (the value of a quick-add `due:`).
func inputToken(english string) string {
	return strings.ReplaceAll(tr(english), " ", "")
}

// The three hints below are assembled from the keyword helpers rather than
// spelled out in each translation table. A hint is a promise about what will
// parse, and one written by hand in three tables is a promise that goes stale
// the first time a spelling changes. TestHintsOnlyAdvertiseTokensThatParse
// feeds each of them back through the parsers in every language.

// quickAddHint is the token cheat-sheet under the quick-add field.
func quickAddHint() string {
	return fmt.Sprintf(tr("#tag @project %s p:%s s:l r:%s %s^"),
		inputWord("due:")+inputToken("tomorrow"),
		inputWord("high"),
		inputWord("weekly"),
		inputWord("dep:"))
}

// searchHint is the filter field's placeholder.
func searchHint() string {
	return fmt.Sprintf(tr("Filter... (#tag @project p:%s %s<%s)"),
		inputWord("high"),
		inputWord("due:"),
		strings.ToLower(localizedWeekdayShort(time.Friday)))
}

// invalidDateMsg names the date words in the language they will be accepted
// in, instead of baking English examples into a translated sentence.
func invalidDateMsg() string {
	return fmt.Sprintf(tr("Invalid date - use dd-mm-yy, %q, %q, %q, %q, or '+3d'"),
		inputWord("today"), inputWord("tomorrow"), inputWord("next week"),
		strings.ToLower(localizedWeekday(time.Monday)))
}
