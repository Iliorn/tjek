package app

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Iliorn/tjek/rank"
	"github.com/Iliorn/tjek/todo"
)

// ── claim / release ──────────────────────────────────────────────────────────

// exitClaimed is the exit code of a claim that was not made: the task is
// someone else's, or (claim --next) no task was free. It is not an error, so
// an agent can tell "pick another" from "something broke".
const exitClaimed = 3

// defaultClaimTTL is how long a claim lasts unless --ttl says otherwise:
// long enough for a task's worth of work between renewals, short enough
// that an agent that died frees its task before anyone has to notice.
const defaultClaimTTL = 30 * time.Minute

// taskJSON is a task as --json shows it, with its claim when it has one.
type taskJSON struct {
	todo.Todo
	Claim *claimJSON `json:"claim,omitempty"`
}

func withClaim(t todo.Todo, claims map[string]claim, now time.Time) taskJSON {
	out := taskJSON{Todo: t}
	if c, ok := claimFor(claims, &t, now); ok {
		out.Claim = c.json()
	}
	return out
}

func tasksJSON(rows []todo.Todo, claims map[string]claim, now time.Time) []taskJSON {
	out := make([]taskJSON, len(rows))
	for i, t := range rows {
		out[i] = withClaim(t, claims, now)
	}
	return out
}

// cliClaims reads the store's live claims for a command that shows tasks.
// A store from before claims, or one that cannot be read, has none: showing
// the tasks matters more than marking them.
func cliClaims(now time.Time) map[string]claim {
	if err := openStore(); err != nil {
		return nil
	}
	claims, err := loadClaims(db, now)
	if err != nil {
		return nil
	}
	return claims
}

// claimHolder is who a claim is made for: --as, or the name this device's
// edits carry (TJEK_AUTHOR first), so an agent that signs its edits as
// itself claims as itself too.
func claimHolder(as string) (string, bool) {
	if as = strings.TrimSpace(as); as != "" {
		return as, true
	}
	settings, _ := loadSettings()
	if n := strings.TrimSpace(authorName(settings)); n != "" {
		return n, true
	}
	fmt.Fprintln(os.Stderr, "tjek: no name to claim as; pass --as NAME or set TJEK_AUTHOR")
	return "", false
}

// claimWhen is a claim's end as text: the time today, the date too beyond it.
func claimWhen(t, now time.Time) string {
	t = t.Local()
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

func cliClaim(args []string) int {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	as := fs.String("as", "", "who the claim is for (default: TJEK_AUTHOR, else your name)")
	ttl := fs.Duration("ttl", defaultClaimTTL, "how long the claim lasts; claim again to renew it")
	next := fs.Bool("next", false, "claim the highest-ranked task that is ready and unclaimed, in place of a ref")
	where := fs.String("where", "", "with --next: only tasks matching a filter in the app's / grammar ('#work p:high')")
	asJSON := fs.Bool("json", false, "emit the claimed task as JSON, as show --json does, with its claim")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: tjek claim <ref> | --next [--where=FILTER]  [--as=NAME] [--ttl=30m] [--json]")
		fs.PrintDefaults()
	}
	flagArgs, positionals := splitFlagsAndPositionals(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	switch {
	case *next && len(positionals) > 0:
		fmt.Fprintln(os.Stderr, "tjek claim: give a ref or --next, not both")
		return 2
	case !*next && len(positionals) != 1:
		fs.Usage()
		return 2
	case *where != "" && !*next:
		fmt.Fprintln(os.Stderr, "tjek claim: --where picks among tasks for --next; a ref names one already")
		return 2
	case *ttl <= 0:
		fmt.Fprintln(os.Stderr, "tjek claim: --ttl must be positive (30m, 2h)")
		return 2
	}
	holder, ok := claimHolder(*as)
	if !ok {
		return 2
	}
	repo, todos, err := loadForCLI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		return 1
	}
	now := time.Now()

	var t *todo.Todo
	var c claim
	if *next {
		t, c, err = claimNext(todos, *where, holder, *ttl, now, repo.ranker())
		if err != nil {
			fmt.Fprintf(os.Stderr, "tjek claim: %v\n", err)
			return 1
		}
		if t == nil {
			fmt.Fprintln(os.Stderr, "tjek claim: no ready task is unclaimed")
			return exitClaimed
		}
	} else {
		t, err = findTaskByRef(todoPtrs(todos), positionals[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if t.Status != todo.Pending {
			fmt.Fprintf(os.Stderr, "tjek claim: %s %q is done; there is nothing left to claim it for\n", shortID(t.ID), t.Title)
			return 1
		}
		var took bool
		c, took, err = takeClaim(db, t.ID, holder, *ttl, now, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tjek claim: %v\n", err)
			return 1
		}
		if !took {
			fmt.Fprintf(os.Stderr, "tjek claim: %s %q is claimed by %s until %s\n",
				shortID(t.ID), t.Title, c.Holder, claimWhen(c.Until, now))
			return exitClaimed
		}
	}
	if *asJSON {
		return emitJSON(withClaim(*t, map[string]claim{t.ID: c}, now))
	}
	fmt.Printf("claimed %s %q for %s until %s\n", shortID(t.ID), t.Title, c.Holder, claimWhen(c.Until, now))
	return 0
}

// claimNext claims the first task `tjek top` would show that is ready (no
// unfinished dependency, not waiting for its start date), unclaimed, without
// a timer running (someone is on it), and matching where. Each candidate is
// tried with takeClaim, so a task another process claimed after the list was
// read is passed over rather than shared. It returns nil when none was free.
// A task holder already has is not taken again: an agent asking for its next
// task wants a new one.
func claimNext(todos []todo.Todo, where, holder string, ttl time.Duration, now time.Time, rk rank.Ranker) (*todo.Todo, claim, error) {
	eligible := map[string]bool{}
	for _, t := range filterTopLevel(todos, listFilterOpts{onlyReady: true, hideWaiting: true, where: where, now: now}) {
		if !t.IsTimerRunning() {
			eligible[t.ID] = true
		}
	}
	held, err := loadClaims(db, now)
	if err != nil {
		return nil, claim{}, err
	}
	for _, t := range rk.Top(todoPtrs(todos)) {
		if !eligible[t.ID] {
			continue
		}
		if _, taken := held[t.ID]; taken {
			continue
		}
		c, took, err := takeClaim(db, t.ID, holder, ttl, now, false)
		if err != nil {
			return nil, claim{}, err
		}
		if took {
			return &t, c, nil
		}
	}
	return nil, claim{}, nil
}

// releaseJSON is one task's line in `tjek release --json`.
type releaseJSON struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Released bool       `json:"released"`
	HeldBy   *claimJSON `json:"held_by,omitempty"`
}

func cliRelease(args []string) int {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	as := fs.String("as", "", "whose claim to release (default: TJEK_AUTHOR, else your name)")
	force := fs.Bool("f", false, "release the claim whoever holds it")
	asJSON := fs.Bool("json", false, "emit what was released as JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: tjek release <ref>... [--as=NAME] [-f] [--json]")
		fs.PrintDefaults()
	}
	flagArgs, positionals := splitFlagsAndPositionals(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positionals) == 0 {
		fs.Usage()
		return 2
	}
	holder, ok := claimHolder(*as)
	if !ok {
		return 2
	}
	_, todos, err := loadForCLI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		return 1
	}
	ptrs := todoPtrs(todos)
	now := time.Now()
	rc := 0
	var out []releaseJSON
	for _, ref := range positionals {
		t, err := findTaskByRef(ptrs, ref)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			rc = max(rc, 2)
			continue
		}
		released, held, err := releaseClaim(db, t.ID, holder, *force, now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tjek release: %v\n", err)
			return 1
		}
		row := releaseJSON{ID: t.ID, Title: t.Title, Released: released}
		switch {
		case released:
			if !*asJSON {
				fmt.Printf("released %s %q\n", shortID(t.ID), t.Title)
			}
		case held.Holder != "":
			row.HeldBy = held.json()
			fmt.Fprintf(os.Stderr, "tjek release: %s %q is claimed by %s until %s; -f releases it anyway\n",
				shortID(t.ID), t.Title, held.Holder, claimWhen(held.Until, now))
			rc = max(rc, exitClaimed)
		default:
			fmt.Fprintf(os.Stderr, "tjek release: %s %q is not claimed\n", shortID(t.ID), t.Title)
		}
		out = append(out, row)
	}
	if *asJSON && rc == 0 {
		return emitJSON(out)
	}
	return rc
}
