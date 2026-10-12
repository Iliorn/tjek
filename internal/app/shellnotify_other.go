//go:build !windows

package app

// Only Explorer caches a binary's icon by its path; see shellnotify_windows.go
// for what this stands in for.

func notifyShellChanged(string) {}
