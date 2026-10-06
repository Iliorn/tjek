package app

import (
	"fmt"
	"strings"

	"github.com/Iliorn/tjek/todo"
)

// recurrence.go reads a recurrence rule as typed and shows one as stored. The
// rules themselves, and the series they describe, are todo's (recur.go);
// this is the part that speaks the user's language.

// recurRuleHelp is the rule grammar as the CLI's help and errors spell it.
const recurRuleHelp = "daily|weekdays|weekly|monthly|yearly|Nd|Nw|Nm|Ny, weekly on days as mon,thu or 2w:mon,thu, " +
	"ending with /until:DATE or /count:N"

// parseRecurInput canonicalizes a rule as typed (r:, recur:, --recur, the
// detail field): the English grammar todo.ParseRule reads, with the active
// language's words for its keywords and weekdays, and any date tjek takes
// for an end ("until:+3m", "indtil:fredag"). The empty string parses as
// ("", true), which clears a rule.
func parseRecurInput(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", true
	}
	parts := strings.Split(s, "/")
	parts[0] = canonicalRecurBase(parts[0])
	for i, p := range parts[1:] {
		p = canonicalInputToken(p)
		// A date as stored (yyyy-mm-dd) is left for todo.ParseRule, which
		// reads it; parseDueDate takes the ones typed elsewhere in tjek.
		if v, ok := strings.CutPrefix(p, "until:"); ok {
			if d, err := parseDueDate(v); err == nil {
				p = "until:" + d.Format("2006-01-02")
			}
		}
		parts[i+1] = p
	}
	return todo.ParseRecurrence(strings.Join(parts, "/"))
}

// canonicalRecurBase is the interval part of a typed rule with its words in
// English: "ugentlig" → "weekly", "man,tor" → "mon,thu", "2w:man" →
// "2w:mon". Words it does not know are left for todo.ParseRule to refuse.
func canonicalRecurBase(s string) string {
	if i := strings.LastIndexByte(s, ':'); i >= 0 && isDayList(s[i+1:]) {
		return canonicalRecurBase(s[:i]) + ":" + canonicalDayList(s[i+1:])
	}
	if isDayList(s) {
		return canonicalDayList(s)
	}
	return canonicalInputWord(s)
}

// isDayList reports whether s is a comma-separated list of weekdays in any
// spelling parseWeekday takes.
func isDayList(s string) bool {
	for _, d := range strings.Split(s, ",") {
		if _, ok := parseWeekday(d); !ok {
			return false
		}
	}
	return true
}

// canonicalDayList rewrites a list of weekdays to the three-letter English
// keys a rule stores; an unknown day is left as typed, for the rule parser
// to refuse.
func canonicalDayList(s string) string {
	days := strings.Split(s, ",")
	for i, d := range days {
		if wd, ok := parseWeekday(d); ok {
			days[i] = strings.ToLower(wd.String()[:3])
		}
	}
	return strings.Join(days, ",")
}

// trRecurrence renders a recurrence rule for display: the interval in the
// active language ("every:Nd|w|m|y" kept as-is, since the prefix is a
// recognizable English keyword and the number+unit is locale-neutral), then
// its weekdays and its end. A rule this build cannot read shows as stored.
func trRecurrence(rule string) string {
	r, ok := todo.ParseRule(rule)
	if !ok {
		return rule
	}
	parts := []string{tr(r.Interval())}
	if days := r.Weekdays(); len(days) > 0 {
		names := make([]string, len(days))
		for i, d := range days {
			names[i] = strings.ToLower(localizedWeekdayShort(d))
		}
		parts = append(parts, strings.Join(names, ","))
	}
	if !r.Until.IsZero() {
		parts = append(parts, tr("until")+" "+r.Until.Format("02-01-06"))
	}
	if r.Count > 0 {
		parts = append(parts, fmt.Sprintf(tr("%d times"), r.Count))
	}
	return strings.Join(parts, " ")
}

// recurInputValue is a rule as the detail field's editor shows it: the
// stored form, with an end date in the dd-mm-yy tjek takes dates in.
func recurInputValue(rule string) string {
	r, ok := todo.ParseRule(rule)
	if !ok || r.Until.IsZero() {
		return rule
	}
	return strings.Replace(rule, "until:"+r.Until.Format("2006-01-02"), "until:"+r.Until.Format("02-01-06"), 1)
}
