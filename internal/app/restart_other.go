//go:build !windows

package app

import (
	"fmt"
	"os"
	"syscall"
)

// afterDataMove ends a run that moved tjek's data (moved) or tried to. After
// a move this process is replaced by a fresh tjek, with the same arguments
// and environment, which opens in the new place. It returns only when there
// is nothing to start, or starting failed, with the code to exit with.
func afterDataMove(moved bool) int {
	if !moved {
		return 1
	}
	exe, err := os.Executable()
	if err == nil {
		err = syscall.Exec(exe, os.Args, os.Environ())
	}
	fmt.Fprintf(os.Stderr, tr("Start tjek again to open it there (%v)."), err)
	fmt.Fprintln(os.Stderr)
	return 0
}
