//go:build !windows

package app

import (
	"os"
	"path/filepath"
)

// desktopDir is ~/Desktop; empty when there is no home to look in.
func desktopDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Desktop")
}
