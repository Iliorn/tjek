//go:build !windows

package app

import (
	"fmt"
	"os"
	"syscall"
)

// restartSelf replaces this process with a fresh tjek, with the same
// arguments and environment. It returns only when that fails, with the code
// to exit with.
func restartSelf() int {
	exe, err := os.Executable()
	if err == nil {
		err = syscall.Exec(exe, os.Args, os.Environ())
	}
	fmt.Fprintf(os.Stderr, tr("Start tjek again to open it there (%v)."), err)
	fmt.Fprintln(os.Stderr)
	return 0
}
