package app

import "golang.org/x/sys/windows"

// desktopDir is the user's Desktop wherever Windows keeps it, OneDrive's
// included; empty when it cannot say.
func desktopDir() string {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	if err != nil {
		return ""
	}
	return dir
}
