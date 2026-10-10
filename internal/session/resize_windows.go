package session

import (
	"io"
	"os"
	"time"
)

// sizeDescriptor is the handle the size of the console is read from. Only
// the output of a console knows its screen buffer, so it is stdout when
// that is a file, and stdin otherwise, which then says it is no console.
func sizeDescriptor(stdin *os.File, out io.Writer) int {
	if file, ok := out.(*os.File); ok {
		return int(file.Fd())
	}

	return int(stdin.Fd())
}

// pollInterval is how often the size of the console is read on Windows.
const pollInterval = 200 * time.Millisecond

// notifyResize reports on the returned channel each time the terminal
// changes size, until stop is closed. Windows sends no signal for that, and
// the events of the console would take the keystrokes with them, so the
// size is read now and then.
func notifyResize(fd int, stop <-chan struct{}) <-chan struct{} {
	return pollSize(func() (Size, error) { return sizeOfTerminal(fd) }, pollInterval, stop)
}
