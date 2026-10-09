# The command line

`tjek` with no arguments opens the app; with a command it runs that command
and exits, which makes it scriptable. `tjek help` prints the full reference
and `tjek man` a man page.

```sh
tjek add "Buy milk" --size=s --due=tomorrow --p=high --tag=shopping
tjek list                       # pending top-level tasks (table)
tjek list --json --focus        # JSON, today + overdue only
tjek list --stale=30d --sort=idle --wide   # backlog review: nothing touched in a month,
                                 # longest-untouched first, with AGE and IDLE columns
tjek list --unblocked-since=14d # tasks freed recently: every blocker done, the last one this fortnight
tjek list --waiting             # tasks put away until a later start date (list and top leave them out)
tjek search RAM --word          # whole-word match ("RAM" won't match "Ramte"); --re for a regexp
tjek top -n=5                   # top 5 by sequence score (percent of the current field)
tjek show milk                  # full detail (incl. score breakdown + subtask IDs)
tjek why milk                   # why it ranks there: each factor's cause, the margins to the
                                 # tasks either side, and when the ranking shifts on its own
tjek edit milk --p=high --add-tag=urgent --due=tomorrow
tjek edit cello --wait=someday  # put it away until you bring it back (--wait is --start)
tjek add "Renew passport wait:eom-7d"   # hidden until a week before the month ends
tjek edit deploy --add-dep=sign-off   # depend on another task (refused if it would loop)
tjek edit deploy --remove-dep=sign-off
tjek edit a1b2 c3d4 e5f6 --project=hoth   # one change across several tasks (--title stays single-ref)
tjek add "Rent" --due=31-01-27 --recur=monthly   # due on the 31st, or the month's last day
tjek edit gym --recur=mon,thu/until:30-06-27     # set a rule; the series counts from the due date
tjek edit gym --clear-recur                      # stop repeating
tjek done milk                  # mark a task done
tjek reopen milk                # move it back to pending (the counterpart to done)
tjek delete milk                # soft delete (alias: tjek rm)
tjek subtask milk "find receipt"   # create a subtask of "milk"
tjek add "Deploy release" --depends="sign-off"   # block the new task until "sign-off" is done
tjek start milk                 # start the time tracker
tjek stop                       # stop the running tracker (no ref needed)
tjek comment milk "blocked on review"
tjek comment milk --edit=1 "still blocked, asked Sam"
tjek comment milk --delete=2
tjek stats                      # one-line summary
tjek stats --tag=work           # same, scoped to tasks carrying a tag (also --project / --search)
tjek stats --seq                # sequence miss analysis: which score dimension buried the
                                 # tasks you finished anyway, plus a bias-tuning hint
tjek stats --format=waybar      # Waybar-shaped JSON for a status-bar widget
tjek export tasks.json          # versioned JSON snapshot of the open tasks
tjek export --include-done full.json  # every task, completed ones too (a backup)
tjek import backup.json         # merge an export file into the local store
tjek import - < backup.json     # same, reading from stdin
tjek doctor                     # this installation's health, for bug reports
tjek help
```

## Referring to tasks

A task reference is either the start of its ID (`60b9`) or part of its title
(`milk`, any case). An ID prefix wins, so scripts stay predictable. A
reference that matches several tasks fails with exit code 2 and lists them:

```
$ tjek done milk
title "milk" matches 2 tasks:
    21a164e1  Buy milk
    2ffe832a  Buy more milk
```

Flags can go before or after the reference. The CLI reads the same
`settings.json` as the app, so its ranking matches your bias settings.

The app and the CLI share one database. A running app notices changes made
from the command line (or synced from another device) and reloads, waiting
until you finish any edit you are in the middle of.

A change made from the command line shows in the task's history marked
`cli`, under the name from Settings. Set `TJEK_AUTHOR` to sign a script's
changes with a name of its own; `tjek show` prints the history at the end.

## The `--json` output is a contract

`list`, `search`, `top`, `show`, `why`, `add`, `tags`, `projects`, `doctor`,
`stats --format=json|waybar` and `export` all emit JSON, and that shape is
treated as an interface:

- **Fields are not renamed or removed in a patch or minor release.** New
  fields may be added at any time, so read the keys you need and ignore the
  rest.
- **A removal or rename waits for a major version** and is called out in the
  [changelog](../CHANGELOG.md).
- Every one of those shapes is pinned by golden files
  (`internal/app/testdata/json_contract/`) that CI compares on every run.

Two things worth knowing: timestamps are always present (an unset date is
`0001-01-01T00:00:00Z`, not a missing key), and `size` is absent for
medium-sized tasks, which is the default.

## Shell completion and man page

Both are generated from the same command table the CLI runs from, so they
can't fall behind a new command or flag. Task references complete from your
open tasks.

```sh
tjek completion bash > /etc/bash_completion.d/tjek
tjek completion zsh > "${fpath[1]}/_tjek"
tjek completion fish > ~/.config/fish/completions/tjek.fish
tjek man > ~/.local/share/man/man1/tjek.1
```

## Shared projects

```sh
tjek share                               # the projects this device shares, and where
tjek share start Trip ~/OneDrive         # share Trip in ~/OneDrive/Trip.tjek
tjek share join ~/OneDrive/Trip.tjek     # join the project a file holds
tjek share leave Trip                    # leave it and remove its tasks here
tjek share rename Trip Summer            # rename it for everyone sharing it
tjek share sync                          # sync every shared project now
```

`join` refuses when this device already has tasks in a project of the same
name, since joining hands them to everyone sharing it; `--merge` goes ahead.
A rename reaches the others on their next sync of the project; the file keeps
its name. Every command that changes tasks brings the shared projects up to
date afterwards. A shared project never goes through `tjek sync`: each machine
joins its file. See [Shared projects](guide.md#shared-projects).

## Export and import

`tjek export` writes a versioned JSON envelope to the file it is given, or to
stdout:

```json
{
  "version": 1,
  "exported_at": "2026-07-10T12:00:00Z",
  "tasks": [ ... ]
}
```

The main fields of each task:

| Field | Type | Notes |
|-------|------|-------|
| `id` | string (UUID) | stable identifier, used as the merge key |
| `title` | string | |
| `status` | int | 0 = pending, 1 = done |
| `priority` | int | 0 = low, 1 = medium, 2 = high |
| `size` | int | 0 = medium, 1 = small, 2 = large |
| `created_at` / `modified_at` | RFC 3339 | when the task was made and last changed; sync merges each field on its own |
| `due_date` | RFC 3339 (omitempty) | |
| `project` | string (omitempty) | |
| `tags` | string array (omitempty) | |
| `dependencies` | string array (omitempty) | IDs of tasks this one is blocked by |
| `comments` | array (omitempty) | each has `id`, `text`, `created_at` |
| `deleted` / `deleted_at` | bool / RFC 3339 | deletion markers, so a delete syncs |
| `parent_id` | string (omitempty) | set on subtasks |

`tjek import <file>` (or `tjek import -` for stdin) merges the file into
your tasks with the same merge that powers sync. It **never replaces**
anything wholesale, so `export | import` is always safe and importing the same
file twice changes nothing. A bare JSON array of tasks is accepted too.
