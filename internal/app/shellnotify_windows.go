//go:build windows

package app

import (
	"syscall"
	"unsafe"
)

// shellnotify_windows.go tells Explorer that a file changed. Explorer keeps
// the icon it drew for a path in its icon cache and does not look at the file
// again on its own, so a tjek.exe replaced in place by self-update went on
// showing the icon of the version before it, until the cache was rebuilt by
// hand (ie4uinit.exe -show) or at the next sign-in.
//
// SHChangeNotify is the documented way to say so: SHCNE_UPDATEITEM for the
// file itself, and SHCNE_ASSOCCHANGED, which is what makes Explorer drop its
// cached icons and draw them again. The second redraws every icon on screen
// once, which an update, a rare event, can afford.

const (
	shcneUpdateItem   = 0x00002000
	shcneAssocChanged = 0x08000000
	shcnfIDList       = 0x0000
	shcnfPathW        = 0x0005
)

var procSHChangeNotify = syscall.NewLazyDLL("shell32.dll").NewProc("SHChangeNotify")

// notifyShellChanged is best effort: with no Explorer to tell (Server Core, a
// service) or no such call in shell32, the icon stays as it was, and the
// update it follows is done all the same.
func notifyShellChanged(path string) {
	if procSHChangeNotify.Find() != nil {
		return
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	_, _, _ = procSHChangeNotify.Call(shcneUpdateItem, shcnfPathW, uintptr(unsafe.Pointer(p)), 0)
	_, _, _ = procSHChangeNotify.Call(shcneAssocChanged, shcnfIDList, 0, 0)
}
