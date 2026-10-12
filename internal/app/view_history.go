package app

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Iliorn/tjek/todo"
)

// view_history.go draws a task's history (todo.Event) as the detail pane's
// last section and in `tjek show`: one row per thing that happened, newest
// first, "when  who  what".

// historyMergeWindow is how close together a person's edits must be to share
// a row. Setting a due date and then a priority is one piece of work, and a
// row per field would bury the closes and reopens a reader is looking for.
const historyMergeWindow = 10 * time.Minute

// historyRow is one row of a task's history: an event, or a run of edits
// folded into one.
type historyRow struct {
	at     time.Time
	author string
	source string
	action string
	fields []string
}

// historyRows folds events into rows, newest first. An edit joins the row
// before it when the same person made it the same way within
// historyMergeWindow of that row's latest change; edits right after a task
// was made join its creation and add nothing to it, since "created" already
// says every field was set.
func historyRows(events []todo.Event) []historyRow {
	var rows []historyRow
	for _, e := range events {
		if n := len(rows); n > 0 && e.Action == todo.ActionEdited {
			last := &rows[n-1]
			if (last.action == todo.ActionEdited || last.action == todo.ActionCreated) &&
				last.author == e.Author && last.source == e.Source &&
				e.At.Sub(last.at) <= historyMergeWindow {
				last.at = e.At
				if last.action == todo.ActionEdited {
					last.fields = appendNew(last.fields, e.Fields)
				}
				continue
			}
		}
		rows = append(rows, historyRow{
			at: e.At, author: e.Author, source: e.Source, action: e.Action,
			fields: append([]string(nil), e.Fields...),
		})
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows
}

// appendNew appends the members of add that dst lacks, in order.
func appendNew(dst, add []string) []string {
	for _, a := range add {
		if !slices.Contains(dst, a) {
			dst = append(dst, a)
		}
	}
	return dst
}

// historyFieldNames is how a row names each unit it changed, as a lower-case
// noun that reads after "changed".
var historyFieldNames = map[string]string{
	"title":                "title",
	"notes":                "description",
	"priority":             "priority",
	"size":                 "size",
	"project":              "project",
	"parent":               "parent task",
	"recurrence":           "recurrence",
	"stage":                "stage",
	"due":                  "due date",
	"start":                "start date",
	"done":                 "completion date",
	todo.FieldTags:         "tags",
	todo.FieldDependencies: "dependencies",
}

// historyActionWords is the verb each action reads as.
var historyActionWords = map[string]string{
	todo.ActionCreated:  "created",
	todo.ActionClosed:   "closed",
	todo.ActionReopened: "reopened",
	todo.ActionDeleted:  "deleted",
	todo.ActionRestored: "restored",
}

// historyWhat is a row's "what": the action, and what else changed with it.
func historyWhat(r historyRow) string {
	var changed string
	if len(r.fields) > 0 {
		names := make([]string, len(r.fields))
		for i, f := range r.fields {
			name, ok := historyFieldNames[f]
			if !ok {
				name = f // a unit from a newer build
			}
			names[i] = tr(name)
		}
		changed = fmt.Sprintf(tr("changed %s"), strings.Join(names, ", "))
	}
	if r.action == todo.ActionEdited {
		return changed
	}
	word, ok := historyActionWords[r.action]
	if !ok {
		word = r.action
	}
	if changed == "" {
		return tr(word)
	}
	return tr(word) + " · " + changed
}

// historyWho is a row's "who": the person, marked when the change came from
// the command line or the web page, or "automatic" when tjek made it.
func historyWho(r historyRow) string {
	switch r.source {
	case todo.SourceAuto:
		return tr("automatic")
	case todo.SourceCLI, todo.SourceWeb:
		if r.author == "" {
			return r.source
		}
		return r.author + " · " + r.source
	}
	return r.author
}

// historyWhen is a row's time: the clock alone today and yesterday, the day
// and month this year, and the date before that.
func historyWhen(at, now time.Time) string {
	at = at.In(now.Location())
	day := startOfDay(at)
	today := startOfDay(now)
	switch {
	case day.Equal(today):
		return tr("today") + " " + at.Format("15:04")
	case day.Equal(today.AddDate(0, 0, -1)):
		return tr("yesterday") + " " + at.Format("15:04")
	case at.Year() == now.Year():
		return at.Format("02-01 15:04")
	}
	return at.Format("02-01-06")
}

// historyWhoWidth is the "who" column's width for rows: the widest name,
// capped so one long name cannot push "what" off a narrow pane.
func historyWhoWidth(rows []historyRow) int {
	w := 0
	for _, r := range rows {
		w = max(w, ansi.StringWidth(historyWho(r)))
	}
	return min(w, 16)
}

// historyLines is rows as text, each clipped to width: the detail pane's
// rows and `tjek show`'s alike.
func historyLines(rows []historyRow, now time.Time, width int) []string {
	whenW := 0
	for _, r := range rows {
		whenW = max(whenW, ansi.StringWidth(historyWhen(r.at, now)))
	}
	whoW := historyWhoWidth(rows)
	lines := make([]string, len(rows))
	for i, r := range rows {
		line := padRight(historyWhen(r.at, now), whenW) + "  " +
			padRight(truncate(historyWho(r), whoW), whoW) + "  " + historyWhat(r)
		lines[i] = truncate(line, width)
	}
	return lines
}
