// Package app is tjek: the TUI, the CLI verbs, storage and the sync glue, in
// one package split into files by concern (ARCHITECTURE.md). The command at
// the repository root only calls Main.
package app

import (
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// appVersion is the current build version. The release sets it through the
// root package's main.appVersion, which Main is handed (see version.go).
// When no version is injected, init() in version.go replaces this default
// with whatever the Go toolchain recorded — the module version for a
// `go install module@version`, or the git revision for a local build.
var appVersion = "dev"

// Main runs tjek: the CLI when a subcommand is named, the TUI otherwise.
// version is the build's main.appVersion.
func Main(version string) {
	appVersion = resolveVersion(version, readBuildInfo())

	// Windows terminals get UTF-8 wrong in two ways, and tjek writes UTF-8 —
	// borders, chips and the user's own æøå alike. First thing, before
	// anything can print (console.go; a no-op everywhere else).
	restoreConsole := prepareConsole()
	defer restoreConsole()

	// A read-only run (--read-only or TJEK_READ_ONLY) touches no file, so the
	// housekeeping below waits for one that may.
	args, readOnly := takeReadOnly(os.Args[1:])
	if !readOnly {
		// Remove the binaries earlier Windows self-updates moved aside, if any.
		if execPath, err := os.Executable(); err == nil {
			removeLeftoverBinaries(execPath)
		}

		// An install from before the rename: move its files and its binary to
		// tjek's names before anything looks for them.
		adoptFormerName()
	}

	// CLI mode: when the first arg names a subcommand, run the non-TUI
	// dispatcher and exit. Only bare `tjek` launches the Bubble Tea program
	// below; it takes no arguments, so any other word is a mistake to report.
	if len(args) > 0 || readOnly {
		code := 0
		switch {
		case len(args) == 0:
			fmt.Fprintln(os.Stderr, "tjek: the app does not run read-only; name a command (tjek help)")
			code = 2
		case isCLICommand(args[0]):
			code = runCLI(args, readOnly)
			if !readOnly {
				checkpointStore()
			}
		default:
			code = unknownCommand(args[0])
		}
		restoreConsole() // os.Exit runs no deferred calls
		os.Exit(code)
	}

	// Fail closed when the store was written by a newer tjek. Every other
	// load error is reported inside the TUI, which is right for "the disk is
	// full" but wrong here: the app would come up showing an empty list over a
	// full database, and the first save would write this build's older columns
	// back over it (migrator.go).
	if err := openStore(); errors.Is(err, errSchemaTooNew) {
		fmt.Fprintf(os.Stderr, "tjek: %v\n", err)
		restoreConsole()
		os.Exit(1)
	}

	// Opt-in latency trace (TJEK_TRACE=1). Started before the model so the
	// startup load is in the log too.
	stopTrace := startTrace()
	defer stopTrace()

	m := initialModel(newSQLiteRepo())
	// Live reload of out-of-process writes. Started here, not in initialModel:
	// it holds a file descriptor, and only a running program wants one.
	startModelWatcher(&m)
	// Render one frame before handing over. The first View of a session is the
	// expensive one — it builds every derived cache and fills the string-builder
	// pool — and paying for it here means the user's first keystroke doesn't.
	_ = m.View()
	// The renderer paints on a ticker, so the frame rate is also the worst-case
	// delay between a keystroke and seeing it. Bubble Tea's default is 60 FPS
	// (up to ~17ms of waiting); 120 is its maximum and halves that. The frames
	// themselves are line-diffed and small, so the extra ticks cost nothing
	// when nothing changed.
	opts := append([]tea.ProgramOption{tea.WithAltScreen(), tea.WithFPS(120)}, inputProgramOptions()...)
	p := tea.NewProgram(m, opts...)
	// Windows reads the console through its own file, which costs the resize
	// events the console-event reader would have delivered; see input.go.
	stopResize := startResizePoller(p)
	defer stopResize()
	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		// Last, so it survives Bubble Tea's stack dump on screen.
		noteCrashToUser()
		restoreConsole()
		os.Exit(1)
	}
	// Final best-effort sync on exit so the session's last edits propagate
	// immediately (no-op unless sync is configured).
	maybeAutoSyncCLI()
	checkpointStore()
	// Settings → Data folder: the move waits for the app to have let go of
	// the store, then tjek starts again in the new place.
	if fm, ok := final.(model); ok && fm.moveDataTo != nil {
		stopTrace() // its log is in the state folder, which moves
		code := 1
		if finishDataMove(*fm.moveDataTo) {
			code = restartSelf()
		}
		restoreConsole()
		os.Exit(code)
	}
}
