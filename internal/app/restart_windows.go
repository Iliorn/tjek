package app

import (
	"bufio"
	"fmt"
	"os"
)

// afterDataMove ends a run that moved tjek's data (moved) or tried to. On
// Windows tjek does not start itself again: a process cannot be replaced
// there, and a child started from this console after the app has quit did
// not come up. It says what to do and waits for enter, so a window opened
// from a shortcut does not close over the message.
func afterDataMove(moved bool) int {
	if moved {
		fmt.Println(tr("Done. Start tjek again to open it in its new place."))
	}
	fmt.Print(tr("Press enter to close."))
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	if moved {
		return 0
	}
	return 1
}
