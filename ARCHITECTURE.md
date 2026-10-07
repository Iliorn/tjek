# Architecture

How tjek is put together, and the conventions that are load-bearing rather
than stylistic. Read the section for the area you are touching before changing
it. Each rule carries a short reason; the longer story of how it came about is
in the commit that introduced it.

This is the document [CONTRIBUTING.md](CONTRIBUTING.md) refers to, and the one
[CLAUDE.md](CLAUDE.md) hands to Claude Code. There is one copy so the two
audiences cannot be told different things.

## What this is

`tjek` is a keyboard-driven terminal task manager built with Go and Bubble Tea
(Charm). It is a standalone app with its own SQLite storage, **not** a
Taskwarrior frontend. Beyond tasks it has a calendar/time-tracking view,
projects (Gantt), tags, a kanban board, a stats dashboard, a CLI, cross-device
sync, and in-app self-update.

## Commands

```bash
go build -o tjek .                                       # build (version = "dev")
go build -ldflags "-X main.appVersion=v1.8.0" -o tjek .  # build with a real version
go run .                                                 # build & run
go test ./...                                            # all packages
go test -run TestName ./...                              # one test
go vet ./...
golangci-lint run ./...                                  # config in .golangci.yml
```

CI (`.github/workflows/ci.yml`) runs vet, test and build on ubuntu, windows and
macos runners, a `cross` job that cross-compiles every release target, and a
separate golangci-lint job. The matrix exists so platform-specific code runs
before a release rather than being first compiled by it. `os.UserHomeDir` reads
`%USERPROFILE%` on Windows and `$HOME` elsewhere, so `TestMain` sets both;
`TestStorageStaysInsideTheTestHome` fails if the redirect stops covering a
platform.

Tests live beside the code (`*_test.go`). The Bubble Tea loop has two dedicated
suites: `update_keyscript_test.go` drives real `Update` dispatch with scripted
keys, and `undo_property_test.go` runs randomized op+undo pairs (fixed seeds)
and checks the content digest round-trips. **When adding a modal interaction,
add a script flow for it.**

### Releasing

Pushing a `v*` tag runs `.github/workflows/release.yml`, which cross-compiles
Linux and Windows with the version baked in, creates the GitHub release and
attaches the assets:

```bash
git push origin main          # land the commits first
git tag v1.10.0               # bump from the latest release tag
git push origin v1.10.0       # ← triggers the build + release
```

- **The version lives only in tags.** `appVersion` defaults to `"dev"` and is
  injected at build time. Take the next number from `gh release list`, not the
  local `git tag` (remote tags may be missing locally). Patch bumps for
  stat/layout tweaks, minor bumps for new interactive features.
- **Asset names are load-bearing; never rename one.** An installed binary
  looks for the name it was built with, so a name can be added but not
  changed: `tjek` (Linux x64), `tjek-linux-arm64`, `tjek.exe` (Windows
  x64), plus `SHA256SUMS`. `selfUpdateAsset(goos, goarch)` is the one map
  from platform to asset; a new build target needs a case there.
- **The Windows icon** is `rsrc_windows_amd64.syso` in the module root,
  which `go build` links into a Windows amd64 binary by itself, so the
  release workflow needs no step for it. It is drawn as a pixel grid in
  `scripts/icon/main.go`; after changing the grid, `go run ./scripts/icon`
  rewrites the `.syso` and `packaging/icon/`, and all of them are committed.
  The `.syso` holds the icon alone, no manifest.
- **macOS ships from source** through the `Iliorn/homebrew-tap` repository
  (`brew install iliorn/tap/tjek`). The release workflow's `homebrew` job
  bumps the formula's tarball and checksum, pushing with a deploy key scoped
  to the tap (secret `HOMEBREW_TAP_DEPLOY_KEY`), so a release needs no step
  after the tag. There is no AUR package: Arch uses the Linux binary. Do not
  attach macOS binaries or `.app` bundles to releases; Homebrew installs are
  pointed at Homebrew rather than having their managed files replaced.
- **The former names ship too.** tjek was called taskr, and an install from
  then fetches `taskr`, `taskr-linux-arm64`, `taskr.exe` or `taskr.json`, so
  the release attaches the same bytes under those names and lists them in
  `SHA256SUMS`.
- **Self-update** (Settings → "Update to latest release") reads
  `/repos/iliorn/tjek/releases/latest` over stdlib `net/http`
  (`fetchLatestRelease`, `downloadReleaseAsset`), so it needs no other tool
  installed. `downloadVerifiedAsset` checks the asset against `SHA256SUMS`
  and fails closed. The endpoint needs no auth; the unauthenticated rate limit
  gets its own message. Tests point `releaseAPIBase` at an `httptest` server.
- **A CHANGELOG entry is one line.** The release body is the tag's CHANGELOG
  section (`packaging/release-notes.sh`, tied to the file by
  `TestReleaseNotesExtractionMatchesTheChangelog`), and
  `TestChangelogEntriesAreOneLine` enforces the cap. A release page is read by
  someone deciding whether to upgrade; the argument for a change belongs in its
  commit message.
- A manual release is the same two `go build -ldflags "-s -w -X
  main.appVersion=$V"` invocations fed to `gh release create`. `-s -w` strips
  symbols and DWARF (~30% smaller); dev builds keep them for `dlv` and full
  panic traces.

## Packages

The app is package `app` in `internal/app/`, split into files by concern;
`main.go` at the root only hands it the build's version (`main.appVersion`,
the `-X` target) and calls `Main`, so `go install github.com/Iliorn/tjek@latest`
and the release build are unchanged, and the root holds the documents rather
than two hundred source files. Four packages sit beside it, each with a
boundary the compiler enforces:

- **`todo/`**: the domain: `todo.Todo` and its methods (`Toggle`, `AddTag`,
  `StartTimer`, `IsOverdue`, subtask/comment/time-entry mutations). No Bubble
  Tea, no rendering.
- **`rank/`**: the sequencing engine. It imports only `todo`, so it is
  locale-free and model-free by construction. See *The ranking engine*.
- **`paths/`**: where files live. See *Paths*.
- **`tasksync/`**: the sync engine. See *Sync*.

## The app (`internal/app`)

Standard Bubble Tea MVU with one large `model` struct threaded through
everything.

- **`model.go`**: the `model` struct, the enums (`tab`, `appMode`, `pane`,
  sort modes), message types, `initialModel`, the undo stack, and most pure
  lookup/mutation helpers.
- **`model_layout.go`**: geometry on the model: detail/list heights,
  list-offset clamping, and detail scrolling. The detail pane's scroll is a
  **persistent offset** (`detail.scroll`) that `detailScrollWindow` moves only
  when the cursor comes within `detailScrollMargin` of an edge; deriving the
  top from the cursor would glue the cursor to one row and slide the document
  under it. It runs twice per key: `clampDetailScroll` (from `clampCursors`)
  against an estimate, then `applyDetailScrollN` against the lines actually
  rendered. So `estimateDetailCursorLine` and the `detail*Height` helpers must
  track `view_detail.go` section for section;
  `TestDetailCursorEstimateMatchesTheRenderedDocument` checks by finding the
  `▶` in the rendered pane.
- **`update.go`**: top-level `Update`, list keys, tab switching, editor
  launching, self-update. Row-level task keys (`d`/`t`/`p`/`T`/`r`/`x`) gate on
  `drilledIntoTasks()`, so the Tasks tab and both drill-in lists behave as one.
- **`update_msgs.go`**: the messages that arrive from ticks, commands and
  watchers rather than keys (`handleBackgroundMsg`): timers, saves, sync,
  exports, external reloads, and a minute tick that rolls the derived views
  over at midnight (`dayTick`). `dispatch` answers these in any mode,
  and routes everything else by mode (`updateForMode`).
- **`update_detail.go`**: the detail pane's input side (`updateDetail`,
  `detailAdd`/`detailDelete`, `startEditing`); mirrors `view_detail.go`.
  ←/→ step through `detailSections` in document order and open the section
  at the top of the pane; `detailSectionBar` names them on the pane's first
  row, so the keys say what they do.
- **`update_modes.go`**: text-entry and search modes (`updateInput`,
  `updateSearch`, `updateEditTitle`, …). A new modal handler usually goes here.
- **`view.go`**: top-level `View`, the Tasks tab, shared rendering helpers,
  and the help overlay (`helpBodyLines` builds `helpSec` blocks from the keymap
  registry plus reference sections; `filterHelpSections` serves its `/`
  filter). `TestHelpDocumentsEveryToken` ties the token sections to
  `parseQuickAdd`/`compileSearch`. Other tabs render from `view_lists.go`,
  `view_calendar.go`, `view_board.go` and `view_detail.go`.
- **`cache.go`**: `cacheState`; see *The derived-view cache*.
- **`storage_sqlite.go`**: see *Storage*. **`storage.go`** holds settings
  load/save, the legacy JSON envelope (`taskFile`/`migrate`/`decodeTaskFile`,
  now only an import source), and the task comparators.
- **`helpers.go`**: parsing (quick-add syntax, dates, time-entry edits),
  formatting, column layout, editor resolution, self-update file operations.
- **`cli.go` + `cli_*.go`**: the command-line verbs, one file per group:
  `cli.go` (dispatch table, `help`, `update`, shared helpers), `cli_add.go`,
  `cli_edit.go`, `cli_lifecycle.go` (done, reopen, delete, undelete, undo),
  `cli_time.go`, `cli_query.go` (list, search, tags, projects, top),
  `cli_show.go` (show, why, stats). A new verb goes in the group it reads like,
  and into `dispatchCLI` and `cliCommandSpecs`. The CLI stays English: it never
  calls `applyLang`.
- **`cli_refs.go`**: ref resolution, `loadForCLI`, and the list filter behind
  `list` and `search` (`listFilterOpts`/`filterTopLevel`), including the
  review filters (`staleFor`, `unblockedFor`, word and regexp matching) and the
  CLI-only sorts in `sortTodosByCLIMode` (age/idle/pri; the TUI's sort modes
  must each line up with a visible column). `now` is injectable for tests.
- **`completion.go`**: `tjek completion bash|zsh|fish` and `tjek man`,
  generated from `cliCommandSpecs`. `TestCompletionMatchesFlagSets` compares
  the table with each command's real `flag.FlagSet`;
  `TestCompletionCoversEveryCommand` ties it to the dispatch.
- **`keymap.go`**: the keymap registry: every binding with its action id,
  contexts and description. It generates the footer hints, the help overlay
  and the command palette, so a new key is added there first.
  `TestKeymapActionsAreConsistent` keeps one action on one key everywhere.
- **`keys.go`**: keybinding overrides as an overlay on the keymap registry.
  `navAlias` maps j/k to down/up at dispatch (after the override pass, so a
  rebind onto j or k wins). `sanitizeKeyOverrides` drops unknown actions,
  multi-key values, `ctrl+c` and per-context collisions, each with a reason.
  `resolveKeyOverride` translates a pressed key into the registry default the
  `update*` switches expect, so dispatch never learns about rebinding; a key
  that a rebind freed is swallowed. Everything user-facing renders through
  `effectiveKey`, so hints, help and palette move together.
  Settings → Keys (`keys_settings.go`) edits the same overrides: a row per
  `keyPageActions` entry (every single-key action but those on enter/esc),
  enter captures the next key (`modeCaptureKey`), `keyRebindProblem` refuses
  a key already live in any of the action's contexts, backspace restores the
  default.
- **`palette.go`**: the command palette (`ctrl+k`, `modePalette`). Entries
  come from the keymap registry and **press their key** rather than call the
  action, so the palette cannot grow a second code path. Multi-key bindings
  are skipped by `paletteSendable`; the few worth listing are in
  `paletteExtras`. Ranking: whole-query substring, then per-word substring,
  then subsequence for queries of ≤ 3 runes.
- **`suggest.go`**: inline `#tag`/`@project` completion for both the
  quick-add and search fields. `completionMatches` decides which field is
  completing and `applyCompletionKey` drives it, so the two share one
  implementation. It renders on the single footer line
  (`renderQuickAddSuggestions`), which the parse preview takes back once the
  caret leaves the token. Both are gated on `searchUsesTokenGrammar`: true for
  Tasks, Board and Stats, which run `compileSearch`; false for Projects, which
  filters project names.
- **`groups.go`**: **the Tags and Projects tabs are one implementation.** A
  tag and a project are both a named group of tasks, so the row summary
  (`groupSummary`), order (`groupSort`, persisted as `tag_order`/
  `project_order`), hide-finished rule (`visibleGroups`, toggled by `h` via
  `showFinishedGroups`) and the list behind a row (`groupTaskList`) are
  shared. Each tab only says how a task maps to its groups
  (`tagGroupKeys`/`inTagGroup`, `projectGroupKeys`/`inProjectGroup`); the two
  halves must agree, or a row counts a different list from the one enter
  opens. Inside a group, subtasks fold as on the Tasks tab, through the same
  `expandedTasks` and the same ←/→ (`foldsSubtasks`): an unfolded parent is
  followed by all of its subtasks, in the group or not, and
  `groupNestedRows` indents exactly that run. The open tasks are ordered by
  step (`groupSteps`: step 1 waits on nothing, a task comes one step after
  the latest thing it waits on) and within a step in the sequence order. When
  any task waits, the rows draw the dependency view (`groupDepsFor`, carried
  on `listCols.deps`): the step number in place of the ↥/↧ arrow, and that
  arrow before the box of the rows tied to the selected one (↥ what it waits
  on, ↧ what waits on it). What a task waits on and what it blocks
  are columns every list can show (Waits on, Blocks; `cache.dependents`). Summaries are built in
  `refreshCaches` (`refreshGroups`). Both lists
  draw through `renderGroupRows` and their panes through `groupPane`: the
  group's summary over its task list, which enter walks in place, and the group
  list stays above it on both tabs. Stacked, the list and its pane split the
  height by need (`splitStack`): each gets its rows while both fit, and two
  full panels get half each. `tagStackRows`/`projectListOuter` are read by
  the render and the offset clamp alike, so the rows drawn are the rows the
  cursor is kept in. The one difference is the Projects pane's rows
  (`projectPaneRows`): when an open task has a date (`hasDatedOpenTask`), the
  pane is at least `projStripMinWidth` wide and the rows beside it keep every
  column they show at full width (`groupTaskCols`), a timeline strip
  (`renderGanttStrip`) runs beside the task rows, bar for row, and it always
  reaches today (`ganttDateWindow`).
- **`board.go` / `view_board.go` / `update_board.go` / `board_carry.go`**:
  the kanban tab; see *The board*.
- **`input.go`** and **`console.go`** (each with `_windows`/`_other`
  variants): see *Terminals*.
- **`sharedproject.go` / `sharedui.go` / `cli_share.go`**: shared projects
  through one file (`Trip.tjek`), with no server: the project's tasks with
  stamps, tombstones and history. Every device reads and writes it.
  `syncShared` folds the file and any conflict copies a cloud service made
  of it (`sharedConflictCopies`) into the store with `mergeIntoStore` (the
  sync merge, a CRDT), writes the project back when the file differs, and
  removes the copies; a write a service let another replace comes back from
  the device that still holds it. The file is written in one canonical form
  (`encodeSharedFile`: `tasksync.CanonicalJSON`, ID order), or two devices
  holding the same project would each see the other's bytes as a change and
  rewrite the file forever. Board columns are personal: the file carries no
  stage (`withoutStage`), and a merge keeps the one this device gave a task
  (`keepLocalStages`), since people sharing a project need not share
  columns; done is a status and is shared. Which projects are shared lives in the local
  `shared.json`, by name, so a rename travels through the file: r on the
  row asks and renames it for everyone (`renameShared`: shared.json, then
  the tasks, then the file's `Name`, `Renamed` and `Former`), and every other
  device's pass takes it up (`resolveSharedName`: the later rename wins),
  moving its own tasks over and rewriting its undo history to match
  (`followSharedRename`); a project of its own by the new name moves aside
  first (`moveOwnProjectAside`), or it would join the shared one. While a pass moves the
  tasks, the old name is recorded as `Moving` in shared.json and kept off the
  sync server (`keepsOutOfSync`); after it the name is free for a project of
  one's own, and a move cut short is finished by the next pass
  (`finishMoving`). A task the file holds under a former name, from a device
  that had not heard of the rename, moves to the new one (`strays`). A
  subtask in a shared project whose parent is not in it is detached
  (`detachStraySubtasks`), or the others would hold a subtask of nothing. x on the row refuses (`refuseSharedProjectRemoval`), as
  does renaming another project onto a shared one. A task moved out of the project is
  recorded in the file by ID and project stamp (`Departed`, `departures`),
  and a device that still holds it in the project removes it outright, as
  leaving does; without the record the two devices would rewrite the file
  against each other forever. A timer on a shared task is its starter's
  (`timerScope`, by the entry's author): the store's `runningTimers`, the
  t key, `tjek start`/`stop`, the heartbeat and stale-timer recovery act on
  this device's own only, so starting a timer here never stops someone
  else's, and two people can time one task at once. The calendar and the
  tracked-today count are this person's days and show only their own time;
  a task's own total counts everyone's. A shared project never goes through the sync server
  (`keepsOutOfSync`, filtered both ways in `runClientSync`, and in
  `dbStore.MergeIn` when this device is the server), so leaving can
  remove its tasks outright (`removeProjectTasks`, no tombstones, which would
  delete them for everyone on a later join) and remember their IDs until
  then. A task removed outright, by a leave or a move out, leaves the undo
  history too (`Store.forget`, `forgetUndoOf`): an undo would save it as a
  new task, every field stamped now, over everyone's later edits. The app runs `syncAllShared` off the loop after saves
  (`sharedSoon`) and on a poll (`sharedPollMsg`), which also re-reads
  shared.json for what `tjek share` changed meanwhile (`followSharedConfig`), the watcher reloads what it
  merged, and `flushShared` runs on quit; the CLI runs it after every
  mutating command.
- **`trace.go`**: opt-in latency tracing (`TJEK_TRACE=1` →
  `trace.log` in the state dir): per frame, the wall clock, gap since the previous
  frame, `Update` and `View` durations, GC count and message. It writes on its
  own goroutine and drops rather than blocks. Measured: a keystroke costs
  ~0.1ms in `Update` and ~1ms in `View` at 2000 tasks, and an idle app produces
  no messages, so a late keystroke is not the model's compute.
  `TJEK_NO_WATCH=1` removes the file watcher, the app's only continuous OS
  interaction; bisect with it first.
- **`crash.go`**: the panic path. Bubble Tea already recovers panics (on its
  loop and in commands) and restores the terminal, so the guard is deferred
  *inside* `Update` and `View` and re-panics: that is the only place holding
  both the stack from the panic site and the live model. It writes
  `crash-<ts>.log` to the state dir (newest five kept), flushes pending writes,
  and records the path for `main` to print after `Run` returns. `msg` is
  formatted (`msgKind`) only on the panic path, keeping the guard
  allocation-neutral (`BenchmarkView`, `BenchmarkSearchKeystroke`).
- **`exportsettings.go`**: Settings → Export. The auto-export keeps
  `exportFileName` current in a folder: scheduled after a save or an
  external reload (`exportSoon`), soon after the first change and then at
  most once per `exportInterval`, since the folder is usually synced and each
  write is an upload; written atomically off the loop, and on quit by
  `flushPendingWrites`. The import runs `importTasks`, the core `tjek
  import` shares, after saving pending edits, as one undo step naming every
  task in the file. Path prompts complete with `completePath`.
- **`layout.go` / `styles.go` / `constants.go`**: width/height math, theming,
  magic numbers.

## Patterns that matter most

### The derived-view cache

The `Store`'s `tasks` map (`map[string]*todo.Todo`, store.go) is the single
source of truth. Everything the UI shows (active and done lists, tag and
project summaries, the overdue set, subtask progress) is derived and cached on
the model. The store keeps two indexes itself, `subtaskOf` and
`runningTimers`; only its own mutators may write them. After **any** mutation,
call the right invalidator or the UI goes stale:

- `m.markModified(ids...)`: mark tasks dirty for the next save, invalidate
  caches, refresh, re-anchor the cursor. Push the undo snapshot yourself
  (`m.pushUndo`) *before* mutating.
- `m.markCacheDirty()`: caches only; no save.
- `m.markFilterDirty()`: only the filter-derived views, for a changed search
  or focus filter.

`refreshCaches()` rebuilds derived data and calls `followTask`, so the cursor
stays on the same task across re-sorts. **Address tasks by string ID**
(`findTodoByID`, `m.get(id)`, `currentTodo`), never by slice position.

Sorting is the refresh's cost centre. `selectActiveDone` sorts
`[]*todo.Todo` and builds the cached value lists once at the end, because
sorting values moves a 416-byte struct per swap. Every mode's order is one
`less(a, b *todo.Todo)` comparator (`lessByDueDate`, `lessBySize`, the
engine's `LessTie`, …) that ends at `ID`, so each is a total order and
`sort.Slice` suffices. `cache.subProgress` is built in one pass; a missing key
means "no subtasks", so the warm signal is the map being non-nil.

A cursor step must cost well under a frame whatever the set's size, so what a
key reads several times is built once. The drill-in lists
(`groupListMemo` → `cache.groupLists`) and the calendar's day
(`activitiesForDay` → `cache.dayActs`) are memoized and cleared with the rest
of the derived data; a fold goes through `setExpanded`, the one writer of
`expandedTasks`, which clears the group lists too. Tag chips render on demand
per row (`getRenderedTagsForTask`), not for every task on each refresh, and
the Projects pane's timeline is drawn only for the rows the pane shows.
`BenchmarkCursorMove` times one step plus its frame on every surface.

### The file watcher

`watcher.go` turns writes to `tasks.db` by anyone into a reload. Every write
the app makes itself wakes it too, so before reading the store the reload
asks whether anything wrote that memory does not hold (`unseenWrite`):
another process's commit, which moves SQLite's `data_version` (it counts
other connections' commits, and the app has one connection), or one of this
process's own writes that did not come from memory, which bump
`foreignWrites` (`noteForeignWrite`: `mergeIntoStore`, the shared pass's
edits and removals). The app's saves, the timer heartbeat and the score
resync move neither, and cost one query off the loop. The answer is exact:
a time window would take another writer's commit landing just after a save
for that save, and drop it. A reload is also deferred while the user types
(`shouldReloadNow`) and while a save runs (`reloadIfChangedCmd`).

### Cursors are clamped in one place

`clampCursors` runs once at the tail of every `dispatch` and pulls each list
cursor back inside its list. Lists shrink under their cursor constantly (undo,
delete, a narrowing filter, a tab restoring an old cursor), and a cursor past
the end selects nothing. Don't clamp per mutation; a new list with a cursor is
added there. `invariants_test.go` drives randomized keys (fixed seeds) and
checks this plus the store's invariants (`subtaskOf` ↔ `ParentID`,
`runningTimers` ↔ open time entries, no self-dependency) after every key.

It also scrolls each list's window (`listOffset`) to the cursor, against the
rows the list really draws: `taskListRows` for the task lists, measured the
way View lays the screen out (header, footer as drawn, a stacked detail, the
panel's chrome). The renderers draw that many rows, so a list and its clamp
cannot disagree about where the panel ends and leave the selected row just
below it. `scroll_test.go` walks every list, and random keys, checking the
selected row is in the frame.

The **drill-in lists** (Tags/Projects → enter, `drillTaskList`) are not cached;
they re-derive on every read, so `updateList` captures the task ID before a key
and re-follows it after. One level up, the group itself is pinned by name
(`tagPinned`/`projectPinned`): `visibleGroups` keeps a pinned group listed once
finished, and `clampCursors` re-finds it (`followPinnedGroups`).

### State the model owns, and the few globals left

Preferences that code on another goroutine also needs are **values held by the
model and copied out**, never package variables:

- `m.rank`: the ranker (bias knobs, activity heat, the 100% mark), refreshed
  in `refreshCaches`. The repository keeps its own copy through `SetRanker`
  (mutex-guarded, because saves run on a background command), and a sync merge
  scores with biases it is handed.
- `m.boardCfg`: a `boardConfig` (column list, board shown, last edited,
  shared with the fleet). A sync is handed the wire form; an arriving list is
  adopted on the loop.

The CLI has no model: `loadForCLI` puts the ranker on the repository it
returns, and the paths that run beside a command read settings.json directly
(`storedBiases`, `storedBoard`).

What remains global is set once at startup and read on the Update loop,
with one counter besides:

- **Theme.** lipgloss styles are package-level vars reassigned by
  `applyTheme(theme)`; rendering reads them directly. `init()` in `styles.go`
  applies `themes[0]` so styles are never nil in tests.
- **Language.** `activeLang`, set by `applyLang`. See *Localization*.
- **Keybindings.** `activeKeys`, set by `applyKeys` from settings.json
  `"keys"`.
- **`foreignWrites`** (watcher.go), an atomic count of the writes this process
  makes to the store that did not come from memory: a sync merge, a shared
  project's pass, a client's push to the in-process server. Those run on
  goroutines with no model to tell; see *The file watcher*.

### Localization

UI strings are translated gettext-style: the English literal is the key
(`tr("Settings")`), and an untranslated string falls back to English.
`initialModel` applies the stored language, so tests call `applyLang` **after**
building a model. Adding a language = one entry in `translations` plus its
date-name tables (`monthNames`, `weekdayNames`, …; Go's `time` has no locale
support, so name-bearing layouts go through `localized*` helpers).

- **Input is localized; stored data is not.** The quick-add and search grammars
  (`lang_input.go`) accept the active language's keywords (`frist:imorgen`,
  `p:høj`, `überfällig`, weekday names) as well as English. The aliases are
  derived from the translation table itself plus `weekdayNames`/
  `weekdayAbbrevs`, so what the screen prints and what the parser takes cannot
  drift; `extraInputAliases` holds only genuine irregularities. `applyLang`
  rebuilds `activeInputWords`. Adding a keyword = one entry in `inputKeywords`
  plus its translation. Stored values and the sync wire stay canonical English,
  so installs in different languages sync unchanged.
- **Hints are assembled from the grammar.** `quickAddHint`, `searchHint` and
  `invalidDateMsg` are built from the keyword helpers, and
  `TestHintsOnlyAdvertiseTokensThatParse` parses each back in every language.
- **Every string must be translated.** `TestEveryLanguageTranslatesEveryUIString`
  scans for `tr("…")` literals (skipping `lang.go` itself) and enumerates the
  strings that reach `tr` through a variable: keymap descriptions,
  `shortLabel`, help titles, bias levels, the words behind
  `trPriority`/`trSize`/`trRecurrence`. A string that reaches the screen some
  other way goes in `dynamicUIStrings`. Sentences therefore live outside
  `lang.go` (e.g. `trSeqReason` in `view_explain.go`).
- **Translations must fit.** `TestNarrowNoWrapTranslated` compares every tab
  and width with the English baseline for each of `availableLanguages`.
  Shipping languages are Danish and German; German is the width stress case.

## The ranking engine (`rank/`)

- **`score.go`**: the score: five dimensions, three bias knobs, the
  activity-heat snapshot behind Momentum, the `Ranker` value that carries all
  three inputs, hit rate and miss analysis.
- **`explain.go`**: one task's score as a breakdown with per-factor reason
  codes, its position and margins to its neighbours, and a forecast of the
  moments its rank moves on its own (the midnight deadline step, momentum
  expiring). Heat maps each key to its newest signal *instant*, which is what
  lets `Expire`/`HeatExpiries` say when heat runs out.
- **`order.go`**: the lifts a task inherits from its subtasks and from the
  work waiting on it (`Lifts`), the partition that sinks blocked work
  (`DependencySets`), the sequence sort (`SortPtrs`, `SortValues`) and `Top`.

Rules:

- **Sorts score against one instant.** Every sort and ranking takes its score
  function from `ScoreNow()`, never `Score`, which reads the clock per call.
  Age accrues continuously, so two identical tasks scored microseconds apart
  differ by ~1e-11 and never reach the tie-break
  (`TestSequenceSortIsDeterministicForIdenticalTasks`).
- **Displayed scores are percentages.** The raw score is unbounded, so every
  surface that shows one renders `FormatPercent`, a share of the
  highest-scoring pending task. Points appear only where the arithmetic is
  explained (the `w` overlay, `tjek why`, `stats --seq`), and those state
  what 100% currently costs. A hypothetical field (the Settings knob preview)
  passes its own maximum to `PercentOfField`.
- **A task that starts later is out of the field.** It sorts below the work
  that can start today (`Sunk`), so it shows its start day instead of a
  percentage (`startsCell`), and `MaxRanked` leaves it out of the 100% mark.
  Its age counts from the start date (`ageFrom`).
- **The reading side is in the app.** `view_explain.go` owns the sentences
  (`trSeqReason`), the row layout, the `w` overlay and the `tjek why` output,
  so the two surfaces cannot describe one score differently.

## Storage

- **`storage_sqlite.go`** is the SQLite backend behind the `Repository` port
  (repository.go): schema, `openStore`/`openStoreAt`, `sqliteRepo.Save`, row
  encoding, and the first-run import of legacy `tasks.json`.
- **Adding a field to `todo.Todo` requires a migration** and a unit in
  `todo.Fields`. The schema is fully normalized (child records in `task_tags`/`task_comments`/
  `task_time_entries`/`task_dependencies`). A new field needs an
  `internal/app/migrations/NNN_*.sql`, plus wiring into the `sqliteRepo.Save` upsert and the
  `loadTodosCore` scan. A field with only a struct tag silently drops on the
  first round trip.
- **Deletes are tombstones.** `Save` upserts the dirty set and marks the IDs it
  is handed `deleted=1`, so a deletion syncs. The tombstone map carries *when*
  each delete happened (`map[string]time.Time`): saves are debounced, and it
  must be the same instant the undo path clamps against (`touchRestored`).
- **One writer.** A single connection (`SetMaxOpenConns(1)`) serializes writes.
- **Fail closed on a store from the future.** `pendingMigrations` returns
  `errSchemaTooNew` when `schema_version` is newer than this build knows, and
  `main` exits rather than show an empty list over a full database and write
  older columns back.
- **History is written by the save.** `saveStamped` compares each task with
  the stored version (as it does for the stamps) and records one `todo.Event`
  (`historyEvent`): created, closed, reopened, deleted, restored, or edited
  with the changed units. So the TUI, the CLI and an import leave the same
  trail and no mutation site knows about history. The author is the repo's
  `editor` (`SetAuthor` from Settings or `TJEK_AUTHOR`; `newCLIRepo` marks the
  CLI's); a change tjek makes itself is flagged `todo.Todo.Auto` where it
  happens (taskops.go, recurrence, auto-close), never stored, cleared by
  `drainDirty`. Events are insert-only (`task_events`, `INSERT OR IGNORE`), a
  sync merge unions them by ID and records none of its own, and the save hands
  each task back with its full stored history, which `saveDoneMsg` merges into
  the live store (`adoptSaved`), with the authors it gave new comments and
  time entries (`signNewChildren`). `view_history.go` folds runs of edits
  into rows (`historyRows`) for the detail pane's last section and `tjek show`.
- **Saves are debounced and differential.** Mutations set
  `dirty`/`savePending`; a `saveTickMsg` (300ms) drains the change set with
  `Store.drainDirty()` (deep copies, so the save goroutine never reads a task
  being edited) and hands it to `Repository.SaveOnto` on a background command.
  Quitting calls `flushPendingWrites` synchronously. Never write the store
  synchronously from `Update`. Saves run one at a time (`beginSave`,
  `saveFinished`), and a reload waits for a running save and is read again
  if one started behind it (`reloadCmd`, `reloadedMsg.epoch`): a read that
  missed a save would put the edits it wrote back to before them.
- **A save writes edits, not copies** (`rebase.go`). A sync, a shared
  project's pass or another process can change a stored task while the app
  holds an older copy, and saving that copy whole would stamp its stale
  fields as new edits and tombstone the comments it never saw. So the store
  keeps each task's base (`Store.base`: the version this copy last agreed
  with the store on, set on load, reload and save), and the save applies
  only what differs from it to the row as stored now (`rebase`): each unit of
  `todo.Fields`, each tag and dependency, each comment and time entry by ID.
  The result comes back into memory the same way (`adoptSaved`), as does a
  reload under a task still being edited. A task with no base (new here, or
  restored after a delete) is saved whole, as the CLI's short-lived saves are.
  `TestMonkeyEditsAreStoredAsShown` drives random edits over a real store and
  checks each save stores what the screen showed.

## Paths (`paths/`)

Resolve every path through `paths.Dir`/`paths.For`/`paths.Ensure`, never
`os.UserHomeDir` plus a literal. There are four kinds (config, data, state,
cache), mapped to XDG, `%APPDATA%`/`%LOCALAPPDATA%` or `~/Library`. Two
overrides come first: `TJEK_HOME` collapses all four into one directory, and
an existing `~/.tjek` pins an old install there for good (no migration to the
split layout, by design). An install made as taskr is carried over once at
startup (`adoptFormerName`, `paths.AdoptFormerDirs`): each taskr directory is
renamed to its tjek name when that name is free, and a binary still called
taskr renames itself. Only the data directory is created eagerly, so a new writer calls
`paths.Ensure(kind)`.

Data lives at `<data>/tasks.db` (WAL, so `-wal`/`-shm` sidecars). **Tests must
not touch the real home:** `TestMain` (`main_test.go`) and `setTestHome`
redirect `$HOME` and neutralize `XDG_*`/`TJEK_HOME`, and
`TestStorageStaysInsideTheTestHome` walks every path the app can write.

## Sync (`tasksync/` and the app's glue)

`tasksync` is the one package where a bug loses data rather than mis-renders a
list, so it is held to high coverage. It holds the pure merge fold (`Merge`),
the `/v1/sync` protocol (`Request`/`Response`, `PostSync`), the HTTP `Server`,
real-time push (`Hub` for SSE, `Listener` for the client), conflict detection
(`DroppedLocalEdits`, the recovery net behind sync.log) and the digest helpers.
It is storage- and UI-free: its only demand on the app is the one-method
`Store` interface (`dbStore` over `mergeIntoStore`). SQL, file paths, config
and Bubble Tea glue stay in the app.

- **Two versions, two questions.** `ProtocolVersion` is the wire *format*; a
  mismatch is refused with a 409 before merging. `VersionHeader`
  (`Tjek-Version`) is the *build*, stamped by `stampVersion` on every
  response, including a bare 500 from `http.Error`, which is what a stale
  server answers after a newer build migrated its store. `PostSync` puts it in
  the error text (`serverError`) and on `Response.ServerVersion`, where
  `VersionGapWarning` warns about the case that does *not* fail: an additive
  migration an old server silently drops. Package text is English and
  unlocalized; the app wraps it in a translated frame.
- **Bodies are gzipped where both ends agree** (`compress.go`). Responses need
  no negotiation. Requests are compressed only toward a server that advertised
  `Accept-Encoding: gzip`; a 400/415 to a compressed request drops the
  capability and resends plain once. The 64 MB request cap applies after
  decompression (`cappedReader`). The SSE stream is never compressed.
- **`servetls.go`**: https for headless `tjek serve`
  (`--tls-cert`/`--tls-key`). The pair is loaded before binding, then re-read
  by `certReloader.getCertificate` whenever a file's mtime moves, since
  `tailscale cert` and Let's Encrypt renew in place. A failed reload keeps the
  last good pair. The TUI's in-process server stays plain; `healthAnswers`
  probes plain, then https.
- **The board's column list syncs** (`boardsync.go`, `tasksync/board.go`) as
  one optional field each way (`Request.Board`/`Response.Board`), so a stage
  name means the same column on every device. No protocol bump: an older peer
  ignores the field, and a client that sends none means "leave my columns
  alone". `MergeBoard`: the later edit wins, and **a zero timestamp never
  wins**, so a device still on the defaults has nothing to say. The edit stamp
  is set only by `applyStageEdit`. The server keeps the fleet's list in
  `board.json`. An adopted list does not re-stage cards: an unknown stage falls
  into the first column.
- **Every field merges on its own stamp** (`hlc/`, `todo/fields.go`,
  `stamps.go`, `tasksync/merge.go`). A task carries one hybrid-logical-clock
  stamp per merge unit: each of `todo.Fields` (a field, or fields that change
  together), each tag and dependency, and a membership baseline (`SetsKey`).
  `Merge` keeps each unit from the later stamp, so edits to different fields
  of one task on two devices both survive, in any sync order; it is
  commutative, associative and idempotent (`TestMergeIsACRDT`). Stamps are set
  in one place: `saveStamped`, behind `sqliteRepo.Save`, compares each task
  with the stored version and stamps only what differs, so the code that
  edits tasks knows nothing of them. A merge writes the stamps it received and
  moves the device's clock (`hlc_clock`) past them, so an edit made after
  seeing another device's edit stamps later whatever the wall clocks say. A
  task saved before stamps reads them from its modification and deletion
  times (`todo.Stamp`), the whole-task order it had, and so does anything an
  older peer sends. `TestFieldsCoverTheTodo` fails for a `todo.Todo` field in
  no unit.
- **First sync asks before uploading** (`syncadopt.go`). The merge is a union
  by ID, so a device's first sync hands every local task to every other device,
  with no undo. `firstSyncNeedsChoice` (never synced, has live tasks, no answer
  recorded) is the gate. Unattended syncs (TUI launch and timer, CLI
  after-command) decline with `firstSyncNotice`; manual `tjek sync` goes
  through `resolveFirstSync` and needs `--adopt-local` or `--adopt-remote`.
  The answer is stored in sync.json (`Adopted`), so losing the state directory
  does not ask again. `--adopt-remote` exports a backup, clears local rows
  outright (no tombstones: these IDs never left the machine), then pulls.

## The board

The kanban tab (tab 5). Its configuration is a `boardConfig` on the model.

- **The last column is Done.** It is `Status==Done`, not a stored stage, but
  its heading is renameable. Every lookup goes through the config's `pending`
  and `doneColumn`; `stageIndex` and `canonicalStage` search pending columns
  only, so no pending task can be filed under Done and `--stage <Done>` cannot
  complete one. `setStages` runs `ensureDoneColumn`, so "at least one working
  column, then Done" holds whatever set the list.
- **Editing columns** (Settings → "Board columns", `modeEditStages`,
  `applyStageEdit`) carries a renamed column's cards over with `stageRemap`.
- **Column icons.** `[x] Name` in the editor (`parseStagesInput`) gives a
  working column a one-cell mark (`validStageIcon`), kept in
  `boardConfig.icons` by lower-cased name and synced as `Board.Icons`. Once
  any column has one, `statusBox` (the TUI rows and `tjek list` alike)
  shows the task's column mark instead of ready/started/overdue; done is
  always ✓, and the Done column's heading always carries it (`columnIcon`).
  That ✓ is fixed: the editor neither shows nor takes it, and no working
  column may use it (`errDoneIconTaken`).
- **Columns are a projection** of the same filtered, cached lists the Tasks tab
  shows, so `/` on the Board is the shared search and there is no board-only
  filter state.
- **One close path.** `closePendingTask` (update_board.go) is the only
  pending→done transition. Tasks `d`, Board `d` and a card dropped in Done all
  use it, or timer/subtask/rank/recurrence handling forks.
- **Carrying** (`modeBoardCarry`): enter picks a card up, ←/→ carry it,
  enter/esc put it down. Nothing is stored until the drop
  (`boardColumnsForView` only draws the held card over its target), so the trip
  is one undo step; the drop goes through `boardPlaceCard` like H/L. A held
  card is lit in the carry colour (`board_carry.go`), green with ✓ over Done.
- **Layout.** Columns are an even grid (`boardColWidths`), so the board does
  not shift under a carried card. Cards are rounded boxes (`renderBoardBox`:
  border colour carries overdue/timer/done; the bottom edge carries project and
  due, `boardBoxBottom`). `chooseBoardCardLayout` steps down to one-line boxes,
  then compact boxes (the title in the top edge, two rows a card), then plain
  rows, all or nothing across the visible columns. Columns scroll
  through a window (`boardWindow`, clamped by `clampBoardWindow` from
  `clampCursors`), and a tall column scrolls its cards (`boardCardWindow`,
  `clampBoardCardScroll`), both against the render's own `boardGeometry`.
  Below `boardMinWindowCols` visible columns it falls back to
  `renderBoardStacked`.
- Space opens a read-only card view (`modeBoardCard`, `renderBoardCardView`);
  editing stays in the Tasks detail pane. `a` files a new card into the focused
  column (`board.addCol`).
- **Hiding the board** (settings `board_disabled`) removes the tab from the
  bar, tab cycling, the digit keys and the palette (`tabVisible`), and the
  detail pane's Stage row (`stageFieldVisible`). Tab numbers never renumber;
  they are part of the translated labels (`tr("6 Stats")`).
  Settings `groups_disabled` does the same for the Tags and Projects tabs
  together, plus the detail pane's Project row and Tags section
  (`detailSectionShown`).

## Terminals

- **Keyboard (`input.go`).** Off Windows the files are stubs. On Windows,
  Bubble Tea's console-event reader polls with a 16ms sleep, so the first key
  after a pause lags; `tea.WithInputTTY()` opens `CONIN$` and reads the VT
  stream instead. That path has no resize events, so the Windows build polls
  the console size (`startResizePoller`). `TJEK_WIN_CONSOLE_INPUT=1` goes back.
- **Encoding (`console.go`).** A Windows console decodes output with its code
  page (CP850 on a Danish install), which garbles UTF-8. `useUTF8Console` sets
  the output and input code pages to 65001 for the run and restores them on
  every exit path, since `os.Exit` skips defers. `tjek doctor` reports the
  page. mintty (Git Bash, MSYS2) is not a console: its charset comes from the
  locale, so `prepareConsole` sends OSC 701 (`minttyUTF8Sequence`, only when
  `MSYSTEM`/`TERM_PROGRAM` say mintty and stdout is a tty). That one is not
  restored on exit.

## Other conventions

- **Settings is one pane of grouped rows** (`settingsGroups`, view_lists.go).
  A row in no group is never drawn, so a new setting needs a group entry.
  The pane's first row names the groups (`settingsSectionBar`, the detail
  pane's `sectionBar`), and below it the pane shows only the cursor's group,
  a page per group, so the bar says what is on screen. ←/→ or pgup/pgdn
  turn the page (`settingsGroupJump`), as ←/→ step the detail pane's
  sections; ↑/↓ past a group's last row continue onto the next page.
  `settingsNavOrder` skips rows that `settingsSelectable` rejects (Version) or
  `settingsRowVisible` hides (Listen and Server token while no server runs).
  `renderSettingsSection` returns the content *and* the cursor row's line,
  which the pane scrolls by. `settingsEditsText` marks rows whose enter opens
  an editor. All value changes go through `settingsAdjust(dir)`: enter steps
  forward, backspace back. The detail pane's stepped fields (recurrence,
  priority, size, stage) answer the same two keys (`detailStepValue`), so
  ←/→ mean section in both panes and never change a value. Recurrence also
  takes `r`, which opens its rule as text, for the rules no step reaches.
- **Modes drive input.** `m.mode` (an `appMode`) picks the `update*`/`render*`
  path. A feature with text entry or a confirm prompt adds an `appMode`, a
  handler (usually `update_modes.go`) and a render branch.
- **Subtasks and dependencies share the task set** (a subtask is a full `Todo`
  with a `ParentID`), so global operations loop the whole set
  (`renameTagGlobally`, `summarizeGroups`). Three fields are tree-scoped: a
  deadline runs *up* (`extendAncestorsDue`), priority runs *down*
  (`clampPriorityToParent`, `clampDescendantsPriority`; raising a child
  never lifts a parked parent, and the TUI says the child was capped), and a
  project runs *down* too (`propagateDescendantsProject`), since a subtask is
  a step of its parent's work. All live in `taskops.go` and are called from
  the TUI (`cyclePriority`, `setProject`) and the CLI (`editOneTask`) alike.
  A new subtask copies the parent's context (`InheritContextFrom`); its tags
  only while settings `subtask_tags_disabled` is off.
- **Detail placement is one predicate.** `detailPos` (settings
  `detail_position`: `right`/`left`/`bottom`) feeds `sideBySide()`; bottom
  makes it false at every width, and left swaps the two sized panels at the end
  of `buildSideBySide`. Absent or unknown values read as `bottom`, the
  default; every settings save writes the word, so an install that has run
  keeps its placement. It holds on every tab:
  a task opened from a tag's or project's list (`drillDetailOpen`) is laid out
  as the Tasks tab is, with that list (`drillListLines`) in the list's place,
  so no tab places its detail by a rule of its own. A stacked task detail
  shares the height with the list above it as the group tabs do
  (`stackedTaskDetailLines` through `splitStack`): a short list keeps its
  rows and the detail takes the rest, two full panels get half each. The list
  keeps the open task in view (`clampCursors`).

## Rendering conventions

- **Every panel has a blank row inside each end**, under its border title
  and above its bottom border (`Padding(1, 1)` in `styles.go`). Height math
  takes a panel's chrome from `panelChromeLines` and `panelContentHeight`,
  never a literal.
- **ANSI-aware width math.** After a lipgloss `.Render`, `len([]rune(s))`
  counts escape sequences. Measure styled text with `ansi.StringWidth` and clip
  it with `ansi.Truncate`. Width tests assert no line exceeds the pane's inner
  width (`termWidth-8`): the no-wrap contract.
- **Shared name column.** The leading column on Tasks/Projects/Tags is sized by
  `contentFitWidth` (layout.go). Reuse it for a new list tab.
- **Small terminals are supported.** Width budgets go negative on a tiny
  window, so `truncate`/`padRight`/`padLeft`/`padCenter` clamp negative widths
  to zero. Two-column layouts need a narrow fallback, not floors
  (`buildCalendarNarrow` below `calSideBySideMinWidth`,
  the Projects pane's timeline below `projStripMinWidth`, `renderBoardStacked`).
  `smallterm_test.go` sweeps every tab × state × size from 0×0 for panics and
  the no-wrap contract.
- **Group same-style runs.** Coalesce consecutive same-styled cells into one
  `.Render` (`statsCell`/`renderCellRow`): fewer escape sequences, honest
  widths.
- **A task row is two tones.** `renderTaskLineWithSet` builds rows through
  `rowBuf`, which counts width on the unstyled text and coalesces runs by SGR
  prefix/suffix. `taskRowPalette` gives `status` (normal/overdue/blocked/timer,
  with the selection background) to gutter, checkbox and title, and `meta`
  (dim) to Score, Size and Project. The Due cell takes the status tone only
  when the task is late, so red in that column means the date is the problem.
  Task rows (active, subtask, history) draw no `cursorMark`: the full-width
  selection band marks the row, and the gutter holds the fold sign (`+`/`-`).
- **Titles clip; badges don't.** `taskRowLabel` splits a label into prefix,
  title text and badges (`!`, `↥`, `↧`, `↻`, `(1/2)`); `fitTaskRowLabel` clips
  only the text. `refreshTaskColMetrics` sizes the column from the same
  function, so a new badge is one edit.
- **Columns are a setting.** `listColumns` (settings `columns`, the Settings
  page Columns) switches the optional columns, and `taskListColsShown` leaves
  out the ones switched off, so every list that sizes through it follows.
  A column the file does not name keeps its default
  (`listColumnsFromSettings`), so one added later arrives as its default. The
  columns read off the task (and the caches) are `extraColumns`: a key, a header, a
  width and a value, drawn after the fixed ones and dropped first when the
  pane is narrow. A new one is an entry there and in `listColumnKeys`.
  The Tasks tab's `s` steps through Sequence and the sort of each column
  shown (`taskSorts`, `taskSortsShown`); switching off the sorted column puts
  the list back on Sequence (`settleTaskSort`). A sort mode is stored by
  number, so a new one is appended.
- **The title takes width first; tags degrade.** `taskListCols` reserves at
  most `tagsReservePct` for tags (floored at `tagsOverflowMinW`). The tags cell
  is sized per row at render time (`renderRowTags` → `renderTaskTagsClipped`),
  dropping chips from the end behind a `+N` count.
- **No glyph a terminal may draw double-width.** Symbols with an emoji face
  (`⚠`, `⚡`, anything ≥ U+1F000) are out of the UI; arrows, box drawing, `✓`
  and `▶` are fine. `TestUIAvoidsGlyphsTerminalsDrawDoubleWide` scans the
  sources. User text is data and renders however the terminal draws it.
- **One ellipsis, one cell.** Clipped strings end in `ellipsis` (`…`).
  `truncate` counts runes; `truncateStyled`/`truncateLines` go through `ansi`,
  and `truncateLines` carries the marker too.
