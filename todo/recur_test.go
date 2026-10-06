package todo

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// series is the first n instances of rule anchored at from after it, as
// dates, each worked out from the one before, as closing every instance on
// its due day would.
func series(t *testing.T, rule string, from time.Time, n int) string {
	t.Helper()
	r, ok := ParseRule(rule)
	if !ok {
		t.Fatalf("ParseRule(%q) failed", rule)
	}
	var out []string
	at, k := from, 0
	for range n {
		next, nk, ok := r.Next(from, at, k)
		if !ok {
			out = append(out, "end")
			break
		}
		out = append(out, next.Format("2006-01-02"))
		at, k = next, nk
	}
	return strings.Join(out, " ")
}

func TestRuleNextWalksTheSeries(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	mon := day(2026, 6, 15) // a Monday
	for _, c := range []struct {
		rule string
		from time.Time
		want string
	}{
		{"daily", mon, "2026-06-16 2026-06-17 2026-06-18"},
		{"every:3d", mon, "2026-06-18 2026-06-21 2026-06-24"},
		{"weekly", mon, "2026-06-22 2026-06-29 2026-07-06"},
		{"every:2w", mon, "2026-06-29 2026-07-13 2026-07-27"},
		{"weekdays", day(2026, 6, 18), "2026-06-19 2026-06-22 2026-06-23"},
		{"weekdays", day(2026, 6, 20), "2026-06-22 2026-06-23 2026-06-24"}, // from a Saturday
		// The month's last day stands in for a day it lacks, and the series
		// is back on its day the month after.
		{"monthly", day(2027, 1, 31), "2027-02-28 2027-03-31 2027-04-30 2027-05-31"},
		{"monthly", day(2028, 1, 30), "2028-02-29 2028-03-30 2028-04-30"},
		{"every:3m", day(2027, 11, 30), "2028-02-29 2028-05-30 2028-08-30"},
		{"yearly", day(2028, 2, 29), "2029-02-28 2030-02-28 2031-02-28 2032-02-29"},
		{"every:4y", day(2028, 2, 29), "2032-02-29 2036-02-29"},
		// Chosen weekdays, in the anchor's week and on.
		{"weekly:mon,thu", mon, "2026-06-18 2026-06-22 2026-06-25 2026-06-29"},
		{"weekly:mon,thu", day(2026, 6, 19), "2026-06-22 2026-06-25 2026-06-29"}, // from a Friday
		{"weekly:sun", mon, "2026-06-21 2026-06-28"},                             // weeks run Monday to Sunday
		{"every:2w:mon,thu", mon, "2026-06-18 2026-06-29 2026-07-02 2026-07-13"},
		{"every:2w:mon", day(2026, 6, 18), "2026-06-29 2026-07-13"}, // the anchor's week counts, though its Monday is past
		// Ends: the anchor is the first of count.
		{"daily/count:3", mon, "2026-06-16 2026-06-17 end"},
		{"weekly:mon,thu/count:3", mon, "2026-06-18 2026-06-22 end"},
		{"weekly/until:2026-06-29", mon, "2026-06-22 2026-06-29 end"},
		{"monthly/until:28-02-27", day(2027, 1, 31), "2027-02-28 end"},
	} {
		if got := series(t, c.rule, c.from, 4); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s from %s: %s, want %s", c.rule, c.from.Format("2006-01-02"), got, c.want)
		}
	}
}

// Next counts from the anchor, wherever the instance before was moved to.
func TestRuleNextCountsFromTheAnchor(t *testing.T) {
	r, _ := ParseRule("monthly")
	from := time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct{ after, want time.Time }{
		// January's instance done late, on 2 February: February's is next.
		{time.Date(2027, 2, 2, 0, 0, 0, 0, time.UTC), time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC)},
		// March's, on the 31st: April's is next, on the 30th.
		{time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC), time.Date(2027, 4, 30, 0, 0, 0, 0, time.UTC)},
		// The anchor itself is never next, though it falls after the day.
		{time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC), time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC)},
	} {
		if got, _, ok := r.Next(from, c.after, 0); !ok || !got.Equal(c.want) {
			t.Errorf("Next(after %s) = %s, want %s", c.after.Format("2006-01-02"), got.Format("2006-01-02"), c.want.Format("2006-01-02"))
		}
	}
}

// A series keeps its time of day across a daylight saving switch.
func TestRuleNextKeepsTheClockAcrossDST(t *testing.T) {
	cph, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	r, _ := ParseRule("weekly")
	from := time.Date(2027, 3, 25, 0, 0, 0, 0, cph)
	got, _, _ := r.Next(from, from, 0)
	if want := time.Date(2027, 4, 1, 0, 0, 0, 0, cph); !got.Equal(want) {
		t.Errorf("Next = %s, want %s", got, want)
	}
}

func TestNextRecurrence(t *testing.T) {
	now := time.Date(2027, 5, 12, 15, 0, 0, 0, time.Local) // a Wednesday
	day := func(m time.Month, d int) time.Time { return time.Date(2027, m, d, 0, 0, 0, 0, time.Local) }
	for _, c := range []struct {
		name            string
		rule            string
		due, from, done time.Time
		index           int
		want            time.Time
		wantIndex       int
		end             bool
	}{
		{name: "from the due date", rule: "weekly", due: day(5, 14), want: day(5, 21), wantIndex: 1},
		{name: "from the anchor, not the moved due date", rule: "monthly", due: day(5, 3), from: day(1, 31), want: day(5, 31), wantIndex: 4},
		// The 19 May instance, pulled forward to the 14th: the 19th is its
		// own place, so the 26th is next.
		{name: "moved earlier: not its own place again", rule: "weekly", due: day(5, 14), from: day(5, 5), index: 2, want: day(5, 26), wantIndex: 3},
		// The 19 May instance, pushed back to the 22nd: the 26th is next.
		{name: "moved later: after its due date", rule: "weekly", due: day(5, 22), from: day(5, 5), index: 2, want: day(5, 26), wantIndex: 3},
		{name: "count: places, not dates", rule: "weekly/count:3", due: day(5, 14), from: day(5, 5), index: 2, end: true},
		// A place on a task with no anchor is stray (a merge, an old peer):
		// the task is the anchor.
		{name: "no anchor: the task is the anchor", rule: "weekly", due: day(5, 14), index: 5, want: day(5, 21), wantIndex: 1},
		// The weeks and days left behind count as places all the same.
		{name: "overdue: not into the past", rule: "weekly", due: day(4, 1), want: day(5, 13), wantIndex: 6},
		{name: "overdue: today is fine", rule: "daily", due: day(4, 1), want: day(5, 12), wantIndex: 41},
		{name: "no due date: from the day it was done", rule: "weekly", done: now, want: day(5, 19), wantIndex: 1},
		{name: "ended", rule: "weekly/until:2027-05-20", due: day(5, 14), end: true},
	} {
		td := Todo{Recurrence: c.rule, DueDate: c.due, RecurFrom: c.from, RecurIndex: c.index, CompletedAt: c.done}
		got, _, index, ok := td.NextRecurrence(now)
		if c.end {
			if ok {
				t.Errorf("%s: next %s, want the series to end", c.name, got)
			}
			continue
		}
		if !ok || !got.Equal(c.want) || index != c.wantIndex {
			t.Errorf("%s: next %s #%d (ok %v), want %s #%d", c.name, got, index, ok, c.want, c.wantIndex)
		}
	}
}

// Closing an instance walks its series from the anchor; this is a daily
// series ten years old, the longest walk a person is likely to make.
func BenchmarkNextOfALongSeries(b *testing.B) {
	from := time.Date(2016, 1, 1, 0, 0, 0, 0, time.Local)
	after := from.AddDate(10, 0, 0)
	for _, rule := range []string{"daily", "weekdays", "weekly:mon,thu", "monthly"} {
		r, _ := ParseRule(rule)
		b.Run(rule, func(b *testing.B) {
			for b.Loop() {
				r.Next(from, after, 0)
			}
		})
	}
}

// Walking from near a date finds the instances, and numbers them, as
// walking from the anchor does.
func TestEachFromNearMatchesTheWalkFromTheAnchor(t *testing.T) {
	rules := []string{"daily", "every:3d", "weekdays", "weekly", "every:3w",
		"weekly:mon,thu", "every:2w:tue,sat,sun", "weekly:sun", "monthly", "every:5m", "yearly"}
	anchor := time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local)
	for _, rule := range rules {
		r, _ := ParseRule(rule)
		for f := 0; f < 400; f += 13 {
			from := anchor.AddDate(0, 0, f)
			for g := -20; g < 800; g += 17 {
				near := from.AddDate(0, 0, g)
				var want, got []string
				collect := func(out *[]string) func(int, time.Time) bool {
					return func(k int, at time.Time) bool {
						if laterDay(at, near) {
							*out = append(*out, fmt.Sprintf("%d:%s", k, at.Format("2006-01-02")))
						}
						return len(*out) < 6
					}
				}
				r.each(from, from, collect(&want))
				r.each(from, near, collect(&got))
				if strings.Join(got, " ") != strings.Join(want, " ") {
					t.Fatalf("%s from %s near %s:\n got %v\nwant %v", rule,
						from.Format("2006-01-02"), near.Format("2006-01-02"), got, want)
				}
			}
		}
	}
}

func TestWorkdaysBefore(t *testing.T) {
	// Count the hard way, around day 0 and far before it.
	for _, start := range []int{-800000, -10, 0} {
		count := 0
		for n := start; n < start+30; n++ {
			if got := workdaysBefore(n+1) - workdaysBefore(start); got != count+boolInt(isWorkday(n)) {
				t.Fatalf("workdays in [%d, %d] = %d, want %d", start, n, got, count+boolInt(isWorkday(n)))
			}
			count += boolInt(isWorkday(n))
		}
	}
}

func isWorkday(n int) bool {
	wd := time.Unix(int64(n)*86400, 0).UTC().Weekday()
	return wd != time.Saturday && wd != time.Sunday
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
