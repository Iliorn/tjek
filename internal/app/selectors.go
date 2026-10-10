package app

import (
	"slices"
	"strings"
	"time"

	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/todo"
)

// Selectors are pure functions that derive a view from the source of truth (the
// task set) plus the relevant UI parameters (search text, sort mode, …). They
// hold no state and touch no model fields, which makes each one independently
// testable and impossible to leave "stale". The cache currently memoizes their
// results; the selectors are the single definition of each derivation.

// ── Predicates ────────────────────────────────────────────────────────────────

func todoMatchesSearch(t todo.Todo, search string) bool {
	return compileSearch(search)(t)
}

// filterEnv is what a filter needs beyond the task in hand. The model fills it
// from its caches (searchEnv); the zero value still filters, with every #tag
// matched as a prefix and nothing counted as blocked.
type filterEnv struct {
	// tagExists reports whether some task carries exactly this tag. A #tag
	// naming one matches only it; anything else matches the tags starting
	// with it, so the list narrows as the tag is typed.
	tagExists func(tag string) bool
	// blocked reports whether a task waits on an unfinished dependency.
	blocked func(id string) bool
}

// filterKind is what a term tests, for the preview's chips.
type filterKind int

const (
	termText filterKind = iota
	termTag
	termProject
	termPriority
	termDue
	termWord
)

// filterTerm is one word of a filter: what it matches, whether it is negated,
// and how the preview names it.
type filterTerm struct {
	kind  filterKind
	neg   bool
	label string
	match func(todo.Todo) bool
}

// compileSearch is compileSearchWith without the model's caches.
func compileSearch(search string) func(todo.Todo) bool {
	return compileSearchWith(search, filterEnv{})
}

// compileSearchWith reads the query once and returns a per-task predicate, so
// the active/done scan doesn't re-parse the (constant) search string for every
// task. The grammar is parseFilter's. The two click-driven sentinels — empty
// (match all) and untaggedKey (no tags) — keep their exact-string meaning.
func compileSearchWith(search string, env filterEnv) func(todo.Todo) bool {
	switch search {
	case "":
		return func(todo.Todo) bool { return true }
	case untaggedKey:
		return func(t todo.Todo) bool { return len(t.Tags) == 0 }
	}
	groups := parseFilter(search, env)
	if len(groups) == 0 {
		return func(todo.Todo) bool { return true }
	}
	return func(t todo.Todo) bool {
	group:
		for _, terms := range groups {
			for _, term := range terms {
				if term.match(t) == term.neg {
					continue group
				}
			}
			return true
		}
		return false
	}
}

// parseFilter reads a query into groups of terms: a task matches when every
// term of some group holds. The word "or" (in any shipped language) separates
// the groups, and the words of a group are ANDed, so AND binds tighter, as in
// Taskwarrior. It reuses the quick-add vocabulary:
//
//   - #tag, @project, p:high, due:<date (comparison "<", ">", "<=", ">=" or an
//     exact day), and the words overdue, waiting, blocked, ready and active;
//   - a comma lists alternatives inside one term: #bug,urgent, @work,home,
//     p:high,medium;
//   - a leading - or ! negates a term: -#work, !overdue, -milk;
//   - a bare # or @ is any tag / any project, so -# is untagged;
//   - anything else is title text. A group's plain words are joined and
//     matched loosely against the title (every letter in order, "grcrs" finds
//     "Buy groceries") or as a substring of the notes; a negated word is
//     matched as a plain substring, since leaving out every loose match would
//     leave out far too much.
//
// A lone "-", "!" or "or", as typed on the way to more, adds nothing, and an
// empty group is dropped, so the list does not jump while the query is typed.
func parseFilter(search string, env filterEnv) [][]filterTerm {
	var groups [][]filterTerm
	var terms []filterTerm
	var text []string
	flush := func() {
		if len(text) > 0 {
			q := strings.ToLower(strings.Join(text, " "))
			terms = append(terms, filterTerm{kind: termText, label: strings.Join(text, " "), match: func(t todo.Todo) bool {
				// Notes are matched as a plain substring, not a subsequence:
				// a note is long enough that a fuzzy match over it would hit
				// almost anything.
				return subsequenceFold(t.Title, q) || strings.Contains(strings.ToLower(t.Notes), q)
			}})
			text = nil
		}
		if len(terms) > 0 {
			groups = append(groups, terms)
			terms = nil
		}
	}
	for _, tok := range strings.Fields(search) {
		if canonicalInputWord(strings.ToLower(tok)) == "or" {
			flush()
			continue
		}
		neg := false
		if len(tok) > 1 && (tok[0] == '-' || tok[0] == '!') {
			neg, tok = true, tok[1:]
		} else if tok == "-" || tok == "!" {
			continue
		}
		term, ok := parseFilterTerm(tok, env)
		switch {
		case ok:
			term.neg = neg
			terms = append(terms, term)
		case neg:
			q := strings.ToLower(tok)
			terms = append(terms, filterTerm{kind: termText, neg: true, label: tok, match: func(t todo.Todo) bool {
				return strings.Contains(strings.ToLower(t.Title), q) || strings.Contains(strings.ToLower(t.Notes), q)
			}})
		default:
			text = append(text, tok)
		}
	}
	flush()
	return groups
}

// parseFilterTerm reads one token that is not plain text, reporting false for
// anything else, a mistyped p: or due: value included, which the caller then
// treats as title text.
func parseFilterTerm(tok string, env filterEnv) (filterTerm, bool) {
	// Same two-step as parseQuickAdd: lower once, then fold a localized field
	// prefix back to English so the branches know one spelling.
	lower := canonicalInputToken(strings.ToLower(tok))
	switch {
	case tok == "#":
		// The sigil alone, as typed on the way to a tag: every tagged task,
		// and a title that has the character itself.
		return filterTerm{kind: termTag, label: "#", match: func(t todo.Todo) bool {
			return len(t.Tags) > 0 || strings.Contains(t.Title, "#")
		}}, true
	case tok == "@":
		return filterTerm{kind: termProject, label: "@", match: func(t todo.Todo) bool {
			return t.Project != "" || strings.Contains(t.Title, "@")
		}}, true
	case strings.HasPrefix(tok, "#"):
		type want struct {
			q     string
			exact bool
		}
		var wants []want
		for _, q := range filterValues(tok[1:]) {
			wants = append(wants, want{q, env.tagExists != nil && env.tagExists(q)})
		}
		if len(wants) == 0 {
			return filterTerm{}, false
		}
		return filterTerm{kind: termTag, label: tok, match: func(t todo.Todo) bool {
			for _, tag := range t.Tags {
				tag = strings.ToLower(tag)
				for _, w := range wants {
					if tag == w.q || !w.exact && strings.HasPrefix(tag, w.q) {
						return true
					}
				}
			}
			return false
		}}, true
	case strings.HasPrefix(tok, "@"):
		qs := filterValues(tok[1:])
		if len(qs) == 0 {
			return filterTerm{}, false
		}
		return filterTerm{kind: termProject, label: tok, match: func(t todo.Todo) bool {
			project := strings.ToLower(t.Project)
			for _, q := range qs {
				if strings.Contains(project, q) {
					return true
				}
			}
			return false
		}}, true
	case strings.HasPrefix(lower, "p:"):
		var ps []todo.Priority
		for _, v := range filterValues(strings.TrimPrefix(lower, "p:")) {
			p, ok := parsePriorityFilter(v)
			if !ok {
				return filterTerm{}, false
			}
			ps = append(ps, p)
		}
		if len(ps) == 0 {
			return filterTerm{}, false
		}
		labels := make([]string, len(ps))
		for i, p := range ps {
			labels[i] = trPriority(p)
		}
		return filterTerm{kind: termPriority, label: "p:" + strings.Join(labels, ","), match: func(t todo.Todo) bool {
			return slices.Contains(ps, t.Priority)
		}}, true
	case strings.HasPrefix(lower, "due:"):
		spec := strings.TrimPrefix(lower, "due:")
		f, ok := parseDueFilter(spec)
		if !ok {
			return filterTerm{}, false
		}
		desc, _ := describeDueFilter(spec)
		return filterTerm{kind: termDue, label: desc, match: f}, true
	}
	now := time.Now()
	var match func(todo.Todo) bool
	word := canonicalInputWord(lower)
	switch word {
	case "overdue":
		match = func(t todo.Todo) bool { return t.IsOverdue() }
	case "waiting":
		match = func(t todo.Todo) bool { return rank.StartsLater(&t, now) }
	case "blocked":
		match = func(t todo.Todo) bool { return env.blocked != nil && env.blocked(t.ID) }
	case "ready":
		// What can be picked up now: open, nothing holding it up, and not
		// waiting for a later start.
		match = func(t todo.Todo) bool {
			return t.Status == todo.Pending && !(env.blocked != nil && env.blocked(t.ID)) && !rank.StartsLater(&t, now)
		}
	case "active":
		match = func(t todo.Todo) bool { return t.RunningEntry() != nil }
	default:
		return filterTerm{}, false
	}
	return filterTerm{kind: termWord, label: tr(word), match: match}, true
}

// filterValues splits a term's value on commas into its lower-cased
// alternatives, dropping the empty ones a trailing comma leaves while typing.
func filterValues(s string) []string {
	var out []string
	for _, v := range strings.Split(strings.ToLower(s), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// searchShowsWaiting reports whether a query asks for the tasks waiting for
// their start date, which the lists otherwise leave out (waitingSet).
func searchShowsWaiting(search string) bool {
	for _, tok := range strings.Fields(search) {
		if canonicalInputWord(strings.ToLower(tok)) == "waiting" {
			return true
		}
	}
	return false
}

// waitingSet is the tasks hidden until their start date, Taskwarrior's wait:
// every pending top-level task that starts on a later day, and the subtasks
// under it, which wait with their parent. A subtask's own start date does not
// hide it, since its parent is on the list; it only ranks lower. top counts
// the top-level tasks, which is what the status line reports. The second pass
// walks parents only when something is waiting, so a set with nothing hidden
// costs one date comparison per task.
func waitingSet(all []*todo.Todo, now time.Time) (set map[string]bool, top int) {
	for _, t := range all {
		if t.ParentID == "" && rank.StartsLater(t, now) {
			if set == nil {
				set = make(map[string]bool)
			}
			set[t.ID] = true
			top++
		}
	}
	if set == nil {
		return nil, 0
	}
	parent := make(map[string]string, len(all))
	for _, t := range all {
		if t.ParentID != "" {
			parent[t.ID] = t.ParentID
		}
	}
	for _, t := range all {
		// A parent chain is short, and the step bound guards a cycle that
		// a bad sync could leave behind.
		for id, steps := t.ParentID, 0; id != "" && steps < 64; id, steps = parent[id], steps+1 {
			if set[id] {
				set[t.ID] = true
				break
			}
		}
	}
	return set, top
}

// subsequenceFold reports whether every rune of needle appears in haystack in
// order (not necessarily contiguous), case-insensitively. Empty needle matches.
// This is the fuzzy form of strings.Contains: "grcrs" matches "Buy groceries"
// (every rune must appear, in order — "grcry" does not, there is no trailing y).
func subsequenceFold(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	n := []rune(strings.ToLower(needle))
	i := 0
	for _, r := range strings.ToLower(haystack) {
		if r == n[i] {
			if i++; i == len(n) {
				return true
			}
		}
	}
	return false
}

// parsePriorityFilter maps a `p:` filter value to a priority, reusing the
// quick-add spellings. The bool is false for anything unrecognised so the caller
// can fall back to treating the token as a literal title word.
func parsePriorityFilter(s string) (todo.Priority, bool) {
	switch canonicalInputWord(s) {
	case "high", "h":
		return todo.PriorityHigh, true
	case "medium", "med", "m":
		return todo.PriorityMedium, true
	case "low", "l":
		return todo.PriorityLow, true
	}
	return 0, false
}

// parseDueFilter builds a due-date predicate from a `due:` filter value. An
// optional leading "<", ">", "<=" or ">=" sets the comparison (else exact day);
// the rest is parsed with the same parseDueDate the quick-add path uses. Tasks
// with no due date never match a due: filter. Returns false if the date is
// unparseable so the caller can fall back to a literal title word.
func parseDueFilter(spec string) (func(todo.Todo) bool, bool) {
	op, rest := "=", spec
	switch {
	case strings.HasPrefix(spec, "<="):
		op, rest = "<=", spec[2:]
	case strings.HasPrefix(spec, ">="):
		op, rest = ">=", spec[2:]
	case strings.HasPrefix(spec, "<"):
		op, rest = "<", spec[1:]
	case strings.HasPrefix(spec, ">"):
		op, rest = ">", spec[1:]
	}
	d, err := parseDueDate(rest)
	if err != nil {
		return nil, false
	}
	day := startOfDay(d)
	return func(t todo.Todo) bool {
		if t.DueDate.IsZero() {
			return false
		}
		td := startOfDay(t.DueDate)
		switch op {
		case "<":
			return td.Before(day)
		case "<=":
			return !td.After(day)
		case ">":
			return td.After(day)
		case ">=":
			return !td.Before(day)
		default:
			return td.Equal(day)
		}
	}, true
}

func todoMatchesFocus(t todo.Todo, focus bool) bool {
	if !focus {
		return true
	}
	return t.IsOverdue() || t.IsDueToday()
}

// ── View selectors ────────────────────────────────────────────────────────────

// selectActiveDone splits the top-level (non-subtask) tasks into the active and
// done lists, applying the search filter to both and the focus filter to active
// only. The active list is sorted by sortMode; the done list by historyMode
// (its own dimension). In Sequence mode, parents inherit the max score of their
// descendants for ranking only — the displayed score stays the parent's own —
// so a high-priority subtask pulls its parent up rather than hiding beneath a
// calmer one.
func selectActiveDone(todos []*todo.Todo, now time.Time, score func(*todo.Todo) float64, search string, focus bool, sortMode taskSortMode, historyMode historySortMode) (active, done []todo.Todo) {
	var rollup map[string]float64
	if sortMode == taskSortSequence {
		rollup = rank.Lifts(todos, score)
	}
	return selectActiveDoneRanked(todos, rollup, now, score, search, filterEnv{}, focus, sortMode, historyMode, nil)
}

// selectActiveDoneRanked takes the lift map from its caller. The model computes
// it once per data change and caches it: it depends on the task set, not on the
// filter, so recomputing it inside the per-keystroke search path walked every
// task twice for an answer that had not changed.
//
// hidden is what both lists leave out (listHidden: the waiting set, and what
// the active context does not match), nil to hide nothing. It is applied here
// rather than by narrowing todos, since a hidden task still blocks the
// visible ones that depend on it.
func selectActiveDoneRanked(todos []*todo.Todo, rollup map[string]float64, now time.Time, score func(*todo.Todo) float64, search string, env filterEnv, focus bool, sortMode taskSortMode, historyMode historySortMode, hidden map[string]bool) (active, done []todo.Todo) {
	match := compileSearchWith(search, env)
	// Split and sort as pointers, then materialize once at the end. The caches
	// hold values — they outlive this call and are read while the store mutates
	// — but sorting them as values meant every swap moved a 416-byte struct,
	// which made this the most expensive step of a cache refresh.
	var activeP, doneP []*todo.Todo
	for _, t := range todos {
		if t.ParentID != "" {
			continue
		}
		switch {
		case t.Status == todo.Pending && !hidden[t.ID] && match(*t) && todoMatchesFocus(*t, focus):
			activeP = append(activeP, t)
		case t.Status == todo.Done && !hidden[t.ID] && match(*t):
			doneP = append(doneP, t)
		}
	}
	// Active tasks rank by taskSort; the done list has its own history sort,
	// since the active modes (score, size) carry no meaning once tasks close.
	// Blocked work, and work not due to start until a later day, sinks below
	// everything that can be started today (see rank.SortPtrs). The blocked set
	// is derived from the whole slice, not from activeP: the task holding one
	// up may be a subtask, or filtered out of view.
	_, less, _ := taskSortColumn(sortMode)
	switch {
	case sortMode == taskSortSequence && searchShowsWaiting(search):
		// /waiting lists what is put away, by when it comes back: the
		// Score column shows that day, and someday sorts last.
		sortTodoPtrs(activeP, lessByStart)
	case sortMode == taskSortSequence:
		blocked, _ := rank.DependencySets(todos)
		rank.SortPtrs(activeP, rollup, rank.Sunk(blocked, activeP, now), score)
	case less != nil:
		sortTodoPtrs(activeP, less)
	default:
		blocked, _ := rank.DependencySets(todos)
		rank.SortPtrs(activeP, nil, rank.Sunk(blocked, activeP, now), score)
	}
	sortTodoPtrs(doneP, historyLess(historyMode))
	return todoValues(activeP), todoValues(doneP)
}
