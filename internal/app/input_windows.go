//go:build windows

package app

import (
	"os"
	"sync/atomic"
	"time"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/windows"
)

// resizePollInterval is how often the Windows build checks whether the console
// was resized. Resize is not a keystroke — a fraction of a second late is
// invisible — and the check is one syscall against a handle we already hold.
const resizePollInterval = 250 * time.Millisecond

func inputProgramOptions() []tea.ProgramOption {
	if consoleInputForced() {
		return nil
	}
	// Opening CONIN$ as its own file is what moves Bubble Tea off the polling
	// console-event reader and onto a blocking read of the VT input stream.
	return []tea.ProgramOption{tea.WithInputTTY(), tea.WithFilter(releaseInputOnQuit)}
}

// A blocking read of CONIN$ cannot be cancelled: Bubble Tea's reader for it
// only drops what the read returns after quitting. And on its way out Bubble
// Tea restores the console's input mode, which Windows holds until that read
// returns, so quitting stalled on a blank screen until one more key was
// pressed. So once the app has quit and the program has not ended, tjek
// presses a space into the console itself: the waiting read takes it, and
// the program, having quit, discards it. Each press waits for the shutdown
// to have had its chance first, so none is made unless it is stuck.
var programEnded atomic.Bool

func releaseInputOnQuit(_ tea.Model, msg tea.Msg) tea.Msg {
	if _, ok := msg.(tea.QuitMsg); ok {
		go func() {
			for range 5 {
				time.Sleep(300 * time.Millisecond)
				if programEnded.Load() {
					return
				}
				_ = pressConsoleKey(' ')
			}
		}()
	}
	return msg
}

// noteProgramEnded tells releaseInputOnQuit the program has returned.
func noteProgramEnded() { programEnded.Store(true) }

var writeConsoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleInputW")

// keyEventRecord is an INPUT_RECORD holding a KEY_EVENT_RECORD.
type keyEventRecord struct {
	eventType       uint16
	_               uint16
	keyDown         int32
	repeatCount     uint16
	virtualKeyCode  uint16
	virtualScanCode uint16
	unicodeChar     uint16
	controlKeyState uint32
}

// pressConsoleKey puts one key press of ch into the console's input.
func pressConsoleKey(ch rune) error {
	name, err := windows.UTF16PtrFromString("CONIN$")
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	rec := keyEventRecord{eventType: 1, keyDown: 1, repeatCount: 1, virtualKeyCode: 0x20, virtualScanCode: 0x39, unicodeChar: uint16(ch)}
	var written uint32
	r, _, err := writeConsoleInput.Call(uintptr(h), uintptr(unsafe.Pointer(&rec)), 1, uintptr(unsafe.Pointer(&written)))
	if r == 0 {
		return err
	}
	return nil
}

// startResizePoller replaces the resize events that the console-event reader
// would have delivered. Windows has no SIGWINCH and the VT input stream says
// nothing about the window, so the size has to be asked for.
func startResizePoller(p *tea.Program) (stop func()) {
	if consoleInputForced() {
		return func() {} // the console reader still reports resizes itself
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(resizePollInterval)
		defer t.Stop()
		lastW, lastH := 0, 0
		for {
			select {
			case <-done:
				return
			case <-t.C:
				w, h, err := term.GetSize(os.Stdout.Fd())
				if err != nil || w <= 0 || h <= 0 {
					continue
				}
				if w != lastW || h != lastH {
					lastW, lastH = w, h
					p.Send(tea.WindowSizeMsg{Width: w, Height: h})
				}
			}
		}
	}()
	var stopped bool
	return func() {
		if !stopped {
			stopped = true
			close(done)
		}
	}
}

func inputPathName() string {
	if consoleInputForced() {
		return "console events (TJEK_WIN_CONSOLE_INPUT, polls every 16ms)"
	}
	return "CONIN$ escape sequences (blocking read)"
}
