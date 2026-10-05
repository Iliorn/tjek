package todo

import (
	"fmt"
	"strings"
	"time"
)

// recur.go is the recurrence engine: a rule (Rule), the series it describes
// from an anchor date (Todo.RecurFrom), and the next instance's date.
//
// Every date of a series is worked out from its anchor, never from the
// instance before it. That is what keeps a series where it belongs: a monthly
// task on the 31st falls on the 28th in February and on the 31st again in
// March, a fortnightly one stays on its weeks, and moving one instance's due
// date moves that instance alone. Setting the rule again starts the series
// over from the task's due date (SetRecurrence).
//
// A rule's canonical form, the one stored and synced, is English:
//
//	daily | weekdays | weekly | monthly | yearly | every:<N><d|w|m|y>
//	weekly:<days> | every:<N>w:<days>      days: mon,tue,…,sun
//	followed by any of /until:<yyyy-mm-dd> and /count:<N>
//
// "weekly" alone repeats on the anchor's weekday; with days, on those. An end
// stops the series: until its last day, or after count instances in all, the
// first included.

// Rule is a parsed recurrence rule.
type Rule struct {
	// N and Unit are the interval: every N days, weeks, months or years
	// ('d', 'w', 'm', 'y').
	N    int
	Unit byte
	// Days, for a weekly rule, is the weekdays it falls on, bit i for
	// time.Weekday(i); zero repeats on the anchor's weekday.
	Days uint8
	// Workdays is the "weekdays" rule: every Monday to Friday.
	Workdays bool
	// Until is the last day the series may fall on; zero for none.
	Until time.Time
	// Count is how many instances the series has in all; zero for no limit.
	Count int
}

// Limits on what a rule may say, so a typo cannot ask for a date in the
// year 50000 or a series that takes a long time to walk.
const (
	maxInterval = 999
	maxCount    = 9999
)

var weekdayKeys = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// ParseRule reads a rule in its canonical form or any of the shorthands
// ParseRecurrence takes. The empty string is not a rule.
func ParseRule(s string) (Rule, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "/")
	r, ok := parseRuleBase(parts[0])
	if !ok {
		return Rule{}, false
	}
	for _, p := range parts[1:] {
		switch {
		case strings.HasPrefix(p, "until:") && r.Until.IsZero():
			d, ok := parseRuleDate(strings.TrimPrefix(p, "until:"))
			if !ok {
				return Rule{}, false
			}
			r.Until = d
		case r.Count == 0 && (strings.HasPrefix(p, "count:") || strings.HasSuffix(p, "x")):
			n, ok := parsePositiveInt(strings.TrimSuffix(strings.TrimPrefix(p, "count:"), "x"))
			if !ok || n < 1 || n > maxCount {
				return Rule{}, false
			}
			r.Count = n
		default:
			return Rule{}, false
		}
	}
	return r, true
}

// parseRuleBase reads the interval part of a rule: everything before the
// first "/".
func parseRuleBase(s string) (Rule, bool) {
	switch s {
	case "daily", "day":
		return Rule{N: 1, Unit: 'd'}, true
	case "weekly", "week":
		return Rule{N: 1, Unit: 'w'}, true
	case "monthly", "month":
		return Rule{N: 1, Unit: 'm'}, true
	case "yearly", "year", "annual", "annually":
		return Rule{N: 1, Unit: 'y'}, true
	case "weekdays", "weekday":
		return Rule{N: 1, Unit: 'd', Workdays: true}, true
	}
	// A bare day list ("mon,thu") is weekly on those days.
	if days, ok := parseWeekdays(s); ok {
		return Rule{N: 1, Unit: 'w', Days: days}, true
	}
	spec, days := strings.TrimPrefix(s, "every:"), ""
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		spec, days = spec[:i], spec[i+1:]
	}
	var r Rule
	switch spec {
	case "weekly", "week":
		r = Rule{N: 1, Unit: 'w'}
	default:
		if len(spec) < 2 {
			return Rule{}, false
		}
		n, ok := parsePositiveInt(spec[:len(spec)-1])
		if !ok || n < 1 || n > maxInterval || !strings.ContainsRune("dwmy", rune(spec[len(spec)-1])) {
			return Rule{}, false
		}
		r = Rule{N: n, Unit: spec[len(spec)-1]}
	}
	if days != "" {
		mask, ok := parseWeekdays(days)
		if !ok || r.Unit != 'w' {
			return Rule{}, false
		}
		r.Days = mask
	}
	return r, true
}

// parseWeekdays reads a comma-separated list of three-letter weekday keys
// into a Rule.Days mask.
func parseWeekdays(s string) (uint8, bool) {
	var mask uint8
	for _, d := range strings.Split(s, ",") {
		i := indexOf(weekdayKeys[:], d)
		if i < 0 {
			return 0, false
		}
		mask |= 1 << i
	}
	return mask, mask != 0
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

// parseRuleDate reads an end date: yyyy-mm-dd as stored, or dd-mm-yy and
// dd-mm-yyyy as tjek takes dates elsewhere. It is a day in local time.
func parseRuleDate(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "02-01-06", "02-01-2006"} {
		if d, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}

// Interval is r's interval in canonical form, without its weekdays or end:
// "weekly", "every:2w".
func (r Rule) Interval() string {
	switch {
	case r.Workdays:
		return "weekdays"
	case r.N == 1 && r.Unit == 'd':
		return "daily"
	case r.N == 1 && r.Unit == 'w':
		return "weekly"
	case r.N == 1 && r.Unit == 'm':
		return "monthly"
	case r.N == 1 && r.Unit == 'y':
		return "yearly"
	}
	return fmt.Sprintf("every:%d%c", r.N, r.Unit)
}

// String is r's canonical form.
func (r Rule) String() string {
	var b strings.Builder
	b.WriteString(r.Interval())
	if days := r.Weekdays(); len(days) > 0 {
		b.WriteByte(':')
		for i, d := range days {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(weekdayKeys[d])
		}
	}
	if !r.Until.IsZero() {
		b.WriteString("/until:" + r.Until.Format("2006-01-02"))
	}
	if r.Count > 0 {
		fmt.Fprintf(&b, "/count:%d", r.Count)
	}
	return b.String()
}

// Weekdays is the days a weekly rule falls on, Monday first; none when it
// falls on the anchor's.
func (r Rule) Weekdays() []time.Weekday {
	var out []time.Weekday
	for i := range 7 {
		d := time.Weekday((i + 1) % 7)
		if r.Days&(1<<d) != 0 {
			out = append(out, d)
		}
	}
	return out
}

// Next is the date of the first instance of the series anchored at from
// that falls on a later day than after, false when the series ends before
// one does. The date keeps from's time of day and location. The anchor
// itself is never next: it is the task the series was anchored on, wherever
// its due date has since moved.
func (r Rule) Next(from, after time.Time) (time.Time, bool) {
	var next time.Time
	found := false
	r.each(from, after, func(k int, at time.Time) bool {
		if r.Count > 0 && k >= r.Count || !r.Until.IsZero() && laterDay(at, r.Until) {
			return false
		}
		if k > 0 && laterDay(at, after) {
			next, found = at, true
			return false
		}
		return true
	})
	return next, found
}

// each calls yield with each instance of the series anchored at from, in
// order, numbered from 0 (from itself), until yield returns false. Instances
// on or before near's day may be left out: the walk starts close to near
// rather than at the anchor, so an old series costs what a new one does. The
// numbering counts the ones left out all the same.
func (r Rule) each(from, near time.Time, yield func(k int, at time.Time) bool) {
	y, m, d := from.Date()
	hh, mm, ss := from.Clock()
	day := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, hh, mm, ss, from.Nanosecond(), from.Location())
	}
	if !yield(0, from) {
		return
	}
	gap := dayNumber(near) - dayNumber(from) // days from the anchor to near
	switch {
	case r.Workdays:
		// k counts the workdays after the anchor up to the one yielded.
		start := max(1, gap)
		k := workdaysBefore(dayNumber(from)+start) - workdaysBefore(dayNumber(from)+1)
		for i := start; ; i++ {
			at := day(y, m, d+i)
			if wd := at.Weekday(); wd == time.Saturday || wd == time.Sunday {
				continue
			}
			k++
			if !yield(k, at) {
				return
			}
		}
	case r.Unit == 'd':
		for k := max(1, gap/r.N); yield(k, day(y, m, d+k*r.N)); k++ {
		}
	case r.Unit == 'm' || r.Unit == 'y':
		months := r.N
		if r.Unit == 'y' {
			months *= 12
		}
		ny, nm, _ := near.Date()
		for k := max(1, ((ny-y)*12+int(nm-m))/months); ; k++ {
			// The anchor's day, or the month's last when it is shorter.
			first := time.Date(y, m+time.Month(k*months), 1, 0, 0, 0, 0, time.UTC)
			last := first.AddDate(0, 1, -1).Day()
			if !yield(k, day(first.Year(), first.Month(), min(d, last))) {
				return
			}
		}
	case r.Unit == 'w':
		days := r.Days
		if days == 0 {
			days = 1 << from.Weekday()
		}
		// Weeks run Monday to Sunday, counted from the anchor's; every Nth
		// one holds instances. The anchor's week holds those after it.
		offset := (int(from.Weekday()) + 6) % 7 // the anchor's day in its week
		monday := d - offset
		perWeek, firstWeek := 0, 0
		for i := range 7 {
			if days&(1<<((i+1)%7)) != 0 {
				perWeek++
				if i > offset {
					firstWeek++
				}
			}
		}
		week := max(0, ((gap+offset)/7/r.N-1)*r.N)
		k := 1
		if week > 0 {
			k += firstWeek + (week/r.N-1)*perWeek
		}
		for ; ; week += r.N {
			for i := range 7 {
				at := day(y, m, monday+7*week+i)
				if days&(1<<at.Weekday()) == 0 || 7*week+i <= offset {
					continue
				}
				if !yield(k, at) {
					return
				}
				k++
			}
		}
	}
}

// dayNumber is t's calendar day, read in its own location, as a count of
// days since 1 January 1970.
func dayNumber(t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// workdaysBefore is the number of Mondays to Fridays among the days
// numbered below n (dayNumber), counted from 29 December 1969, a Monday:
// only differences of it mean anything.
func workdaysBefore(n int) int {
	n += 3 // day 0 was a Thursday
	weeks, rest := n/7, n%7
	if rest < 0 {
		weeks, rest = weeks-1, rest+7
	}
	return weeks*5 + min(rest, 5)
}

// laterDay reports whether a falls on a later calendar day than b, each
// read in its own location.
func laterDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	if ay != by {
		return ay > by
	}
	if am != bm {
		return am > bm
	}
	return ad > bd
}
