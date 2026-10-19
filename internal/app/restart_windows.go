package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// restartSelf runs a fresh tjek in this console, with the same arguments and
// environment, and returns its exit code once it ends. Windows cannot
// replace a running process, so this one waits; it holds no file of tjek's
// by then.
func restartSelf() int {
	exe, err := os.Executable()
	if err == nil {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		if err == nil {
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, tr("Start tjek again to open it there (%v)."), err)
	fmt.Fprintln(os.Stderr)
	return 0
}
