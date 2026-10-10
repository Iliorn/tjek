package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Iliorn/tjek/todo"
)

// view_stats_backlog.go — the Stats tab's backlog chart, Taskwarrior's
// burndown: how many tasks were open at the end of each of the last 30 days,
// so whether the pile is shrinking reads at a glance. It is the fourth stop of
// the Activity pane's enter cycle. One series over time, so it is drawn as an
// area in one hue from a zero baseline, with the scale's top and bottom on a
// quiet axis, and labelled directly where it matters: where it started, where
// it is now, and its peak.

// statsBacklogDays is how many days the backlog chart covers.
const statsBacklogDays = 30

// backlogMemo holds one computed backlog series (cache.backlog).
type backlogMemo struct {
	first time.Time
	open  []int
}

// statsBacklog is the open top-level tasks in the Stats scope at the end of
// each of the last statsBacklogDays days, oldest first, ending today, from
// the cache's memo when it has been drawn since the last refresh.
func (m model) statsBacklog() (first time.Time, open []int) {
	if memo := m.cache.backlog; memo != nil && memo.open != nil {
		return memo.first, memo.open
	}
	first, open = m.computeStatsBacklog()
	if memo := m.cache.backlog; memo != nil {
		memo.first, memo.open = first, open
	}
	return first, open
}

// computeStatsBacklog counts each task open from the day it was created until
// the day it was closed, so the series comes from one pass over the tasks and
// one over the days. Days are counted as day numbers (dayNumber), which needs
// no time arithmetic per task and cannot be fooled by a DST change.
func (m model) computeStatsBacklog() (first time.Time, open []int) {
	now := m.frameTime
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	first = today.AddDate(0, 0, -(statsBacklogDays - 1))
	base := dayNumber(first)
	day := func(t time.Time) int { return dayNumber(t.In(loc)) - base }
	diff := make([]int, statsBacklogDays+1)
	for _, t := range m.statsScopedTodos() {
		if t.ParentID != "" {
			continue
		}
		from, until := day(t.CreatedAt), statsBacklogDays
		if from >= statsBacklogDays {
			continue // created after today: a clock ahead of this one
		}
		if t.Status == todo.Done && !t.CompletedAt.IsZero() {
			until = day(t.CompletedAt)
		}
		from = max(from, 0)
		if until <= from {
			continue // closed before the window, or the day it was made
		}
		diff[from]++
		diff[min(until, statsBacklogDays)]--
	}
	open = make([]int, statsBacklogDays)
	run := 0
	for i := range open {
		run += diff[i]
		open[i] = run
	}
	return first, open
}

// dayNumber counts days since a fixed epoch from t's calendar date, so the
// difference of two is the number of calendar days between them.
func dayNumber(t time.Time) int {
	y, mo, d := t.Date()
	// The days-from-civil count, with March as the first month so the leap
	// day falls at the end of the year.
	m := int(mo)
	if m <= 2 {
		y--
		m += 12
	}
	return 365*y + y/4 - y/100 + y/400 + (153*(m-3)+2)/5 + d
}

// statsBacklogTitle is the Activity pane's border title in backlog mode.
func (m model) statsBacklogTitle() string {
	_, open := m.statsBacklog()
	now := open[len(open)-1]
	name := tr("Backlog")
	scope := "[" + tr("Last 30 days") + " · " + trCount("%d open", now, now) + "]"
	legend := "[" + tr("open tasks at the end of each day") + "]"
	budget := m.termWidth - 10
	for _, form := range []string{name + "  " + scope + "  " + legend, name + "  " + scope, scope, name} {
		if ansi.StringWidth(form) <= budget {
			return form
		}
	}
	return name
}

// statsBarRunes are the eighths a column's top cell can be filled to.
var statsBarRunes = []rune(" ▁▂▃▄▅▆▇█")

// renderStatsBacklog draws the backlog chart in the rows the Activity pane
// has: chart rows, the baseline, and one row of labels.
func (m model) renderStatsBacklog() string {
	b := getBuilder()
	defer putBuilder(b)
	first, open := m.statsBacklog()
	peak := 0
	for _, v := range open {
		peak = max(peak, v)
	}
	if peak == 0 {
		b.WriteString("  " + dimStyle.Render(tr("No open tasks in this range.")) + "\n")
		return b.String()
	}

	chartH := max(m.statsChartHeight(), statsChartMinH)
	axis := strconv.Itoa(peak)
	gutter := len(axis) + 1
	avail := m.termWidth - 8 - 2 - gutter
	n := len(open)
	bw := min(max((avail-(n-1))/n, 1), 3)
	if maxN := (avail + 1) / (bw + 1); n > maxN {
		open, first = open[n-maxN:], first.AddDate(0, 0, n-maxN)
		n = len(open)
	}
	chartW := n*bw + (n - 1)
	margin := strings.Repeat(" ", 2+max((avail-chartW)/2, 0))
	bar := statsGradient[7]

	// Each column's height in eighths of a row, from zero.
	eighths := make([]int, n)
	for i, v := range open {
		eighths[i] = (v*chartH*8 + peak/2) / peak
	}
	for r := chartH - 1; r >= 0; r-- {
		label := ""
		if r == chartH-1 {
			label = axis
		}
		var row strings.Builder
		for i, e := range eighths {
			fill := min(max(e-r*8, 0), 8)
			cell := strings.Repeat(string(statsBarRunes[fill]), bw)
			if i > 0 {
				row.WriteByte(' ')
			}
			row.WriteString(cell)
		}
		b.WriteString(margin + dimStyle.Render(padLeft(label, gutter-1)+" ") + bar.Render(row.String()) + "\n")
	}
	b.WriteString(margin + dimStyle.Render(padLeft("0", gutter-1)+" "+strings.Repeat("─", chartW)) + "\n")

	// The direct labels: where the window starts, where it ends, and between
	// them what changed and the peak.
	start, end := first.Format("02-01"), tr("today")
	change := open[n-1] - open[0]
	trend := "→"
	switch {
	case change > 0:
		trend = "▲"
	case change < 0:
		trend = "▼"
	}
	mid := fmt.Sprintf(tr("%d open · %s %+d since %s · peak %d"), open[n-1], trend, change, start, peak)
	space := chartW - len([]rune(start)) - len([]rune(end))
	if ansi.StringWidth(mid)+2 > space {
		mid = ""
	}
	pad := max(space-ansi.StringWidth(mid), 0)
	line := start + strings.Repeat(" ", pad/2) + mid + strings.Repeat(" ", pad-pad/2) + end
	b.WriteString(margin + strings.Repeat(" ", gutter) + statsAxisStyle.Render(line) + "\n")
	return b.String()
}
