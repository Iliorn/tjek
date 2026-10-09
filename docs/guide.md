# Using tjek

The full tour of the terminal app. The [README](../README.md) has the short
version; the [CLI reference](cli.md) covers the `tjek <command>` side.

## The tabs

- **Tasks**: the main list. Add, complete, delete, rename, set priority,
  size (S/M/L), due and start dates. The detail pane (`enter`) holds comments,
  dependencies, subtasks, a description (opened in `$EDITOR`), a live score
  breakdown and the task's [history](#history).
- **Calendar**: a per-day activity timeline with project and tag roll-ups
  and a tracked-time heatmap. Time entries can be edited or deleted in place.
- **Projects**: tasks grouped by project, with a timeline when an open task
  has a date. `enter` walks into a project's tasks, where the task keys
  (`d` done, `t` track, `p` priority, `r` rename, `x` delete, `enter`
  details) all work; `a` adds a task already in that project, `x` on the
  project row clears the project from its tasks.
- **Tags**: the same, grouped by tag. Tags can be renamed, merged or deleted
  across every task; `f` shows a tag's tasks on the Tasks tab as a filter.
- **Board**: a kanban view; see [The board](#the-board).
- **Stats**: a productivity overview with an activity heatmap. It follows
  the active search, so `#tag` scopes every number to that tag.
- **Settings**: the sequencing knobs, theme, language (English, Dansk,
  Deutsch), board columns, sync, and in-app update.

On Tags and Projects, `enter` walks in one level at a time (row, then its
tasks, then the selected task's detail) and `esc` walks back out the same way. Inside,
`→` unfolds a task's subtasks and `←` folds them, as on the Tasks tab.

## Subtasks

A subtask is a full task with a parent. `+` in front of a task means it has
subtasks folded away, `-` that they are showing; `→`/`←` unfold and fold them
on the Tasks tab and inside a tag or project. A new subtask starts with its
parent's project, deadline and tags, and never outranks its parent's
priority. Moving a parent to another project takes its subtasks along. If you
would rather tag each step yourself, turn off Settings → "Subtasks copy
tags".

## Shared projects

A project can be shared with other people through one file you can all
reach, in a shared OneDrive, Dropbox or network folder. There is no server
to set up.

- **Share**: on the Projects tab, select the project and press `S`, then pick
  the folder to put it in (see [Picking a file or folder](#picking-a-file-or-folder)).
  tjek makes the file there, `Trip.tjek` for a project called Trip, and the
  project gets a `⇄` mark.
- **Join**: Settings → Shared projects → "Join a project", and pick the
  `.tjek` file. If you already have tasks in a project with that name, tjek
  asks before sharing them, since everyone sharing it gets them.
- **Leave**: `S` on the shared project again. Its tasks are removed from
  this device, and the others keep theirs. Joining again brings everything
  back, comments and history included.
- **Rename**: `r` on the shared project. tjek asks, then renames it for
  everyone; the others get the new name the next time their tjek looks at the
  file, which keeps its name. Someone who already has a project of their
  own by the new name keeps it, renamed to "Name (2)". `x` (remove the project from all its tasks) is
  refused on a shared project, since it would take every task out of it for
  everyone.

Everything about the project's tasks is shared: fields, subtasks, comments,
tracked time and history, each signed with the name of whoever made it (see
[History](#history)). Changes made at the same time to different fields of
one task both survive, as they do with [sync](sync.md). Your other tasks
stay private. A subtask you put in a shared project while its parent stays
out of it becomes a task of its own, since the others do not have the
parent.

A few things stay each person's own. Board columns: a card you move on your
board does not move on theirs, and a task they add starts in your first
column; done is shared. Timers: a timer is the person's who started it, so
starting yours never stops theirs and two of you can time one task at once;
your calendar shows only your own time, while the task's total counts
everyone's. A task moved out of the shared project leaves the others'
devices, with nothing of it left in the file.

tjek writes the file a few seconds after a change and looks for the others'
changes every 30 seconds while it runs; the command line does both after
every command that changes tasks, and `tjek share sync` does it at once. If
two people's changes reach the cloud at the same moment, the service keeps
both, one as a copy beside the file ("Trip (Mark's conflicted copy).tjek");
tjek merges the copy in and removes it. A change can arrive a little late
that way, but it is not lost.

A shared project travels only through its file, never through your
[sync server](sync.md): each of your own machines that should have it joins
the file too, and leaving on one machine touches no other.

Two things to know. Anyone who can open the file can read and change the
project, just as with any shared file. And a task moved out of the shared
project stays with the others under the project's name.

## History

The last section of a task's detail pane is its history: who created,
changed, closed, reopened, deleted or restored it, and when, newest first.

```
Historik:
  i dag 14:32    Anna         lukkede
  i dag 09:10    Mark · cli   genåbnede
  i går 11:02    Mark         ændrede forfaldsdato, prioritet
  28-09 08:30    automatisk   oprettede
```

- **Every way of changing a task is recorded**: the app, the command line
  (marked `cli`), an import. A comment, tracked time or a new score is not a
  change to the task and adds no row.
- **"automatic"** is tjek's own doing: the next instance of a repeating task,
  a parent closed with its last subtask, a due date or priority carried along
  a tree of subtasks.
- **Edits close together share a row.** Changes one person makes within ten
  minutes read as one row, and edits right after a task was created are part
  of its creation.
- **The name** is Settings → "Your name", or your account's name until you
  set one. A script or an agent can sign as itself with `TJEK_AUTHOR`:
  `TJEK_AUTHOR=Claude tjek done 3f2a`.

History syncs with the task, so every device shows the same rows. Tasks from
before history existed start with none.

## Keyboard shortcuts

| Key | Action |
|-----|--------|
| `a` | Add task |
| `d` | Toggle done |
| `t` | Start/stop time tracking |
| `D` | Set / clear the due date |
| `r` | Rename |
| `x` / `del` | Delete |
| `n` | Edit the description in `$EDITOR` |
| `f` | Focus mode (today + overdue) |
| `h` | Toggle history |
| `s` | Cycle sort: Sequence, then each column shown (Settings → Columns) |
| `w` | Why this rank: the points behind the percentage and what moves it next |
| `/` | Filter |
| `enter` | Open detail view |
| `u` | Undo |
| `↑`/`↓` or `j`/`k` | Move the cursor |
| `tab` / `shift+tab` | Next / previous tab |
| `1–7` | Jump straight to a tab (7 = Settings) |
| `ctrl+k` | Command palette: find any action by name |
| `?` | Show all shortcuts (`/` filters them) |

## What to work on next

The **Sequence** sort ranks pending tasks by a weighted score: deadline,
priority, momentum (what you have been working on), size and age. The Score
column is a percentage of the top task, so 100% is what tjek thinks you
should do now. Tune the weights in Settings (Relaxed / Balanced / Intense per
dimension), and press `w` on any task to see the points behind its
percentage, their causes, the margins to the rows either side, and when the
ranking will move on its own.

Dependencies feed the ranking: a task that blocks others inherits their
urgency, so the prerequisite for an urgent task surfaces right above it. In
the list, `↥` marks a blocker and `↧` a task still waiting on one.

On the Projects and Tags tabs, a group whose tasks wait on each other is
listed in steps: step 1 is what can start now, and each later step waits on
something before it. On the task under the cursor, what it waits on lights
up in blue and what waits on it in yellow. Settings → Columns adds a **Waits
on** column (what each task is waiting for) and a **Blocks** column (what is
waiting for it) to every task list.

## Adding tasks quickly

```
Buy groceries #shopping due:friday p:high size:s @personal
```

The add field understands `#tag`, `@project`, `due:date`,
`p:high/medium/low`, `size:s/m/l`, `r:rule` (see
[Repeating tasks](#repeating-tasks)) and `wait:date` (see
[Waiting until later](#waiting-until-later)). Typing `#` or `@` offers your existing
tags and projects, most recently used first; `tab` inserts the highlighted
one, `↑/↓` pick another. Projects whose name contains a space aren't offered
there, since the field splits on spaces; set those from the detail pane's `@`
picker. Tags are lowercase, and spaces become `-` (`Deep Work` becomes
`#deep-work`).

Dates: `today` · `tomorrow` · `next week` · `monday` · `15-06-25` · `+3d` ·
`+2w` · `+1m` · `-2d` (counting back)

Taskwarrior's period names work too, in every language: `sow`/`eow`,
`som`/`eom`, `soq`/`eoq` and `soy`/`eoy` are the first and last day of this
week, month, quarter and year, `eoww` is this Friday, and `sonw`, `sonm`,
`sonq` and `sony` the first day of the next one. Weeks start on Monday. Each
can be spelled out, `end of month`, `start of next week`, `end of the year`,
and in the add field written as one word or with dashes: `due:end-of-month`.
In Danish and German they read `slut på måneden` (or `ultimo`) and
`Monatsende`. Any date word takes a count after it: `eom-2d`, `friday+1w`.

## Waiting until later

A task with a start date on a later day is put away until that day, like
Taskwarrior's `wait`. It leaves the Tasks list, the board, and the counts and
lists on Tags and Projects, and comes back on its own at midnight of its
start day. Set the date with `wait:` when adding (`Renew passport
wait:sonm`), on the detail pane's **Start date** row, or with
`tjek edit <ref> --start` (`--wait` works too).

`someday` is a start date with no day in mind: `wait:someday` puts a task away
until you bring it back. The Start column shows the word, and the Score
column `∞`.

Put-away tasks are not lost:

- the status line counts them, as `3 waiting`;
- `/waiting` shows exactly those tasks, so you can open one and clear its
  start date, or set it to `today`, to bring it back now;
- `tjek list --waiting` lists them from the command line;
- starting the timer on one brings it back, since work on it has begun.

A hidden task still blocks the tasks that depend on it. Subtasks wait with
their parent; a subtask's own start date only ranks it lower. Settings →
"Hide until start date" turns the hiding off, and tasks with a later start
then stay in the list, ranked below today's work.

## Repeating tasks

`r:` in the add field, or `r` on a task's **Recurrence** field, makes it
repeat; `enter` on that field steps through the common rules. When you finish
one, the next appears with its due date, its subtasks reopened.

| Rule | Repeats |
|------|---------|
| `daily` · `weekly` · `monthly` · `yearly` | every day, week, month or year |
| `weekdays` | Monday to Friday |
| `3d` · `2w` · `6m` · `2y` | every 3 days, 2 weeks, 6 months, 2 years |
| `mon,thu` · `2w:fri` | on those days, every week or every other week |
| `…/until:31-12-27` · `…/10x` | and stops after that date, or after 10 in all |

A series keeps to its days. A monthly task due on the 31st is due on 28
February and on 31 March again. Moving one task's due date, earlier or
later, moves that one only, and the next falls where it would have anyway.
To move the whole
series, set its rule again: it then counts from the task's due date.

## Filtering

`/` filters the list. Words are combined, and use the same vocabulary as
adding:

```
@work p:high due:<friday        # high-priority Work tasks due before Friday
#urgent overdue                 # overdue tasks tagged urgent
grcrs                           # finds "Buy groceries"
```

Supported: `#tag`, `@project`, `p:high/medium/low`, `due:<date`,
`due:>date`, `due:date` (`<=` and `>=` too), the word `overdue`, and the word
`waiting` for the tasks put away until a later start date. A `#` on
its own shows every tagged task, and an `@` every task in a project. Anything
else matches the title loosely (every letter in order, so `dply` finds
"Deploy release") or the description as plain text.

## In your own language

With the interface set to Dansk or Deutsch, adding and searching accept that
language's words too, such as `frist:imorgen p:høj størrelse:lille` or
`fällig:freitag p:hoch überfällig`, and the hints show those spellings.
English always works. Only what you type is translated: tags, repeat rules
and everything else stored stays in English, so devices set to different
languages sync without trouble. The CLI is English throughout.

## The board

A kanban view of your pending tasks: one column per stage, and a last column
holding the completed ones. The default columns are Backlog / In progress /
Review / Done, and every name is yours to change in Settings → "Board
columns" (comma-separated). Renaming a column takes its cards with it.

The last column always means *done*, and its heading always carries a ✓:
calling it "Shipped" changes the name and nothing else. Moving a card into it
completes the task, and moving one out reopens it (after asking).

### Column icons

Give a column an icon by writing it in brackets before the name:

```
[B] Backlog, [>] In progress, [R] Review, Done
```

The icon leads the column's heading, and every task list shows it in the task's
status box, so `[R]` beside a task says it is in Review, on any tab. A column
without an icon leaves the box blank. The icon is one character: a letter, a
digit or a symbol. Emojis are two cells wide in a terminal and are refused, and
✓ is fixed on the last column, so no other column can have it. Once
any column has an icon, the box shows columns only; an overdue task still shows
red. Icons sync with the columns.

- `←/→` switch columns; `H`/`L` move the selected card between them.
- `enter` picks a card up, `←/→` carry it, `enter` or `esc` put it down.
- `a` adds a card to the focused column; space shows a card's details.
- `/` filters every column at once, with the same search as the Tasks tab.

With more columns than fit, the board scrolls sideways and its title says
which slice you are on (`Workflow ‹ 3–8/11 ›`); below three visible columns
it shows the stages as one stacked list instead. A task's stage can also be
changed on the detail pane's **Stage** row with `enter` / `backspace`, or with
`tjek edit <ref> --stage <name>`. Not using kanban? Settings → "Kanban
board" hides the tab and the Stage row.

## Export and import

Settings → Export keeps a copy of all your tasks, finished ones included, in
a folder you choose. In a OneDrive or Dropbox folder it doubles as a backup,
and another tool can read it.

- **Auto-export folder**: press enter and pick the folder. tjek writes
  `tjek-export.json` there straight away, then keeps it current: within a
  minute of a change, and again when you quit. `x` in the picker turns it
  off.
- **Import from file**: press enter and pick a tjek export, or a
  Taskwarrior one saved from `task export` (see
  [From Taskwarrior](cli.md#from-taskwarrior)). Its tasks are
  merged in: new ones are added, ones you already have take the newer
  version, and nothing is deleted, so importing the same file twice changes
  nothing. `u` takes the whole import back.

### Picking a file or folder

Where tjek needs a file or folder, it opens a picker under the panes, headed
with the folder it is in. It opens where you last picked something, and the
first time on your Desktop.

| Key | Does |
|---|---|
| `↑`/`↓` | Move |
| `→` | Open the marked folder |
| `←` | Go up a folder |
| `enter` | Pick the marked file, or in a folder pick, the marked folder |
| `space` | In a folder pick, pick the folder you are in |
| `/` | Type or paste a path instead (`tab` completes names) |
| `esc` | Cancel |

Only the files that fit are picked; the rest show in grey.

The file is the same one `tjek export --include-done` prints, so the
[command line](cli.md#export-and-import) reads and writes it too.

## Custom keybindings

Every binding has an action name, so rebinding one is a line in
`settings.json` (`tjek doctor` prints where that file is):

```json
{
  "keys": {
    "done": "D",
    "search": "s",
    "sort": "/"
  }
}
```

The action names are listed in the `?` overlay. A rebind moves the action:
the old key stops working, and the footer hints, the help overlay and the
command palette all show the new key. An entry that names an unknown action,
uses more than one key, or clashes with another binding in the same view is
ignored with a warning, and so is `ctrl+c`, which always quits. Bindings written
as a pair or range (`←/→`, `H/L`) can't be rebound.
