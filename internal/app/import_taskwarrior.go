package app

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Iliorn/tjek/todo"
)

// import_taskwarrior.go reads Taskwarrior's `task export` into tjek tasks, so
// `task export | tjek import -` moves a Taskwarrior list over, and the
// Settings import takes the same file. It is a translation, not a sync: the
// tasks then go through the ordinary import merge.
//
// Every value is derived from the file alone (the IDs are Taskwarrior's UUIDs,
// a comment's ID is hashed from its text and time, no field reads the clock),
// so importing the same export twice changes nothing, and importing a later
// one updates the tasks that changed.

// twTask is the part of a Taskwarrior task tjek has a place for.
type twTask struct {
	UUID        string         `json:"uuid"`
	Description string         `json:"description"`
	Status      string         `json:"status"`
	Entry       twTime         `json:"entry"`
	Modified    twTime         `json:"modified"`
	End         twTime         `json:"end"`
	Due         twTime         `json:"due"`
	Wait        twTime         `json:"wait"`
	Scheduled   twTime         `json:"scheduled"`
	Start       twTime         `json:"start"`
	Until       twTime         `json:"until"`
	Project     string         `json:"project"`
	Tags        []string       `json:"tags"`
	Priority    string         `json:"priority"`
	Depends     twDepends      `json:"depends"`
	Annotations []twAnnotation `json:"annotations"`
	Recur       string         `json:"recur"`
	Parent      string         `json:"parent"`
}

type twAnnotation struct {
	Entry       twTime `json:"entry"`
	Description string `json:"description"`
}

// twKnownKeys are the fields twTask reads, and Taskwarrior's own bookkeeping,
// which has no meaning outside it. Any other key is a user-defined attribute
// (UDA), which tjek has nowhere to keep; the import names them.
var twKnownKeys = map[string]bool{
	"uuid": true, "description": true, "status": true, "entry": true, "modified": true,
	"end": true, "due": true, "wait": true, "scheduled": true, "start": true, "until": true,
	"project": true, "tags": true, "priority": true, "depends": true, "annotations": true,
	"recur": true, "parent": true,
	"id": true, "urgency": true, "mask": true, "imask": true, "rtype": true, "template": true,
	"last": true,
}

// twTime is a Taskwarrior timestamp, "20260102T150405Z", always UTC.
type twTime struct{ time.Time }

func (t *twTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	for _, layout := range []string{"20060102T150405Z", time.RFC3339} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("unrecognised Taskwarrior date %q", s)
}

// twDepends reads both spellings of depends: a JSON array (Taskwarrior 2.6
// and later) and a comma-separated string (earlier).
type twDepends []string

func (d *twDepends) UnmarshalJSON(b []byte) error {
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*d = list
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*d = nil
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			*d = append(*d, id)
		}
	}
	return nil
}

// isTaskwarriorExport reports whether a JSON array holds Taskwarrior tasks: a
// uuid and a description, where a tjek task has an id and a title.
func isTaskwarriorExport(data []byte) bool {
	var probe []struct {
		UUID        string  `json:"uuid"`
		Description *string `json:"description"`
		Title       *string `json:"title"`
	}
	if json.Unmarshal(data, &probe) != nil || len(probe) == 0 {
		return false
	}
	p := probe[0]
	return p.UUID != "" && p.Description != nil && p.Title == nil
}

// parseTaskwarrior turns a `task export` into tjek tasks. notes say what did
// not come across, one line each, for the import to report.
func parseTaskwarrior(data []byte) (tasks []todo.Todo, notes []string, err error) {
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("malformed Taskwarrior export: %w", err)
	}
	var tw []twTask
	if err := json.Unmarshal(data, &tw); err != nil {
		return nil, nil, fmt.Errorf("malformed Taskwarrior export: %w", err)
	}

	unknown := map[string]bool{}
	for _, fields := range raw {
		for key := range fields {
			if !twKnownKeys[key] {
				unknown[key] = true
			}
		}
	}

	// Recurring templates are not tasks: their rule goes on the pending
	// instance they spawned. Deleted tasks are left out, as an import never
	// deletes.
	rules := map[string]string{}
	keep := map[string]bool{}
	deleted := 0
	for _, t := range tw {
		switch t.Status {
		case "recurring":
			rules[t.UUID] = t.Recur
		case "deleted":
			deleted++
		default:
			keep[t.UUID] = true
		}
	}

	// Each series' rule goes on one pending instance, the latest due: that
	// one spawns the next when it is done, so an extra rule would spawn twice.
	latest := map[string]twTask{}
	for _, t := range tw {
		if !keep[t.UUID] || t.Parent == "" || t.Status == "completed" || rules[t.Parent] == "" {
			continue
		}
		if cur, ok := latest[t.Parent]; !ok || cur.Due.Before(t.Due.Time) {
			latest[t.Parent] = t
		}
	}
	instanceRule := map[string]string{}
	for parent, t := range latest {
		instanceRule[t.UUID] = rules[parent]
	}

	var droppedRules []string
	untilDropped, depsDropped, somedayDue := 0, 0, 0
	for _, t := range tw {
		if !keep[t.UUID] {
			continue
		}
		task := todo.Todo{
			ID:         strings.ToLower(t.UUID),
			Title:      todo.CapitalizeTitle(t.Description),
			Status:     todo.Pending,
			Priority:   twPriority(t.Priority),
			CreatedAt:  t.Entry.Time,
			ModifiedAt: t.Modified.Time,
			Project:    t.Project,
		}
		if task.ModifiedAt.IsZero() {
			task.ModifiedAt = task.CreatedAt
		}
		if t.Status == "completed" {
			task.Status = todo.Done
			task.CompletedAt = t.End.Time
			if task.CompletedAt.IsZero() {
				task.CompletedAt = task.ModifiedAt
			}
		}
		if !t.Due.IsZero() {
			if todo.IsSomeday(t.Due.Time) {
				somedayDue++
			} else {
				task.DueDate = localDay(t.Due.Time)
			}
		}
		task.StartDate = twStart(t)
		for _, tag := range t.Tags {
			if tag = todo.NormalizeTag(tag); tag != "" && !slices.Contains(task.Tags, tag) {
				task.Tags = append(task.Tags, tag)
			}
		}
		for _, dep := range t.Depends {
			if keep[dep] {
				task.Dependencies = append(task.Dependencies, strings.ToLower(dep))
			} else {
				depsDropped++
			}
		}
		for _, a := range t.Annotations {
			key := t.UUID + "\x00" + a.Entry.Format(time.RFC3339) + "\x00" + a.Description
			task.Comments = append(task.Comments, todo.Comment{
				ID:         uuid.NewSHA1(uuid.NameSpaceURL, []byte(key)).String(),
				Text:       a.Description,
				CreatedAt:  a.Entry.Time,
				ModifiedAt: a.Entry.Time,
			})
		}
		if rule, ok := instanceRule[t.UUID]; ok {
			if r, ok := twRecur(rule); ok && !task.DueDate.IsZero() {
				task.Recurrence, task.RecurFrom = r, task.DueDate
			} else {
				droppedRules = append(droppedRules, rule)
			}
		}
		if !t.Until.IsZero() {
			untilDropped++
		}
		tasks = append(tasks, task)
	}

	if deleted > 0 {
		notes = append(notes, fmt.Sprintf("left out %d deleted task(s)", deleted))
	}
	if len(droppedRules) > 0 {
		slices.Sort(droppedRules)
		notes = append(notes, fmt.Sprintf("%d task(s) no longer repeat: no tjek rule for %s",
			len(droppedRules), strings.Join(slices.Compact(droppedRules), ", ")))
	}
	if untilDropped > 0 {
		notes = append(notes, fmt.Sprintf("until: (expiry) dropped from %d task(s)", untilDropped))
	}
	if somedayDue > 0 {
		notes = append(notes, fmt.Sprintf("due:someday dropped from %d task(s)", somedayDue))
	}
	if depsDropped > 0 {
		notes = append(notes, fmt.Sprintf("%d dependency link(s) to deleted tasks dropped", depsDropped))
	}
	if len(unknown) > 0 {
		keys := make([]string, 0, len(unknown))
		for k := range unknown {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		notes = append(notes, "fields tjek has no place for: "+strings.Join(keys, ", "))
	}
	return tasks, notes, nil
}

// twStart is the tjek start date of a Taskwarrior task: its wait (hidden
// until then, which is what a tjek start date does), else its scheduled date
// (the earliest it can begin), else when work on it started. A day without a
// time of its own starts at 09:00, as todo.SetStartDate does.
func twStart(t twTask) time.Time {
	for _, d := range []time.Time{t.Wait.Time, t.Scheduled.Time} {
		if todo.IsSomeday(d) {
			return todo.Someday
		}
		if !d.IsZero() {
			return localDay(d).Add(9 * time.Hour)
		}
	}
	if !t.Start.IsZero() {
		return t.Start.Local()
	}
	return time.Time{}
}

// localDay is the local calendar day d falls on, at midnight, the way tjek
// stores a due date.
func localDay(d time.Time) time.Time {
	l := d.Local()
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.Local)
}

func twPriority(p string) todo.Priority {
	switch strings.ToUpper(p) {
	case "H":
		return todo.PriorityHigh
	case "L":
		return todo.PriorityLow
	}
	return todo.PriorityMedium
}

// twRecurUnits maps Taskwarrior's duration units to a tjek rule's unit and a
// multiplier (a quarter is three months).
var twRecurUnits = map[string]struct {
	unit string
	mult int
}{
	"d": {"d", 1}, "day": {"d", 1}, "days": {"d", 1},
	"w": {"w", 1}, "wk": {"w", 1}, "wks": {"w", 1}, "week": {"w", 1}, "weeks": {"w", 1},
	"mo": {"m", 1}, "mos": {"m", 1}, "mth": {"m", 1}, "mths": {"m", 1}, "month": {"m", 1}, "months": {"m", 1},
	"q": {"m", 3}, "qtr": {"m", 3}, "qtrs": {"m", 3}, "quarter": {"m", 3}, "quarters": {"m", 3},
	"y": {"y", 1}, "yr": {"y", 1}, "yrs": {"y", 1}, "year": {"y", 1}, "years": {"y", 1},
}

var twRecurNamed = map[string]string{
	"daily":      "daily",
	"day":        "daily",
	"weekdays":   "weekdays",
	"weekly":     "weekly",
	"week":       "weekly",
	"sennight":   "weekly",
	"biweekly":   "2w",
	"fortnight":  "2w",
	"monthly":    "monthly",
	"month":      "monthly",
	"bimonthly":  "2m",
	"quarterly":  "3m",
	"quarter":    "3m",
	"semiannual": "6m",
	"annual":     "yearly",
	"yearly":     "yearly",
	"year":       "yearly",
	"biannual":   "2y",
	"biyearly":   "2y",
}

var (
	twRecurCount = regexp.MustCompile(`^(\d+)\s*([a-z]+)$`)
	twRecurISO   = regexp.MustCompile(`^p(\d+)([dwmy])$`)
)

// twRecur turns a Taskwarrior recur value into a tjek rule: the named ones
// (weekly, quarterly, …), a count and a unit (3d, 2weeks, 1mo), and the ISO
// 8601 forms (P1W). Anything shorter than a day, and anything tjek's own
// parser refuses, has no tjek rule.
func twRecur(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	rule, ok := twRecurNamed[s]
	if m := twRecurCount.FindStringSubmatch(s); !ok && m != nil {
		if u, known := twRecurUnits[m[2]]; known {
			n, _ := strconv.Atoi(m[1])
			rule, ok = strconv.Itoa(n*u.mult)+u.unit, true
		}
	}
	if m := twRecurISO.FindStringSubmatch(s); !ok && m != nil {
		rule, ok = m[1]+m[2], true
	}
	if !ok {
		return "", false
	}
	return todo.ParseRecurrence(rule)
}
