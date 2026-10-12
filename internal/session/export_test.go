package session

import (
	"io"
	"time"
)

// DrainDelay is the drainDelay of the tests, shorter than the real one, so
// that the tests that wait for it run fast.
const DrainDelay = 300 * time.Millisecond

func init() {
	drainDelay = DrainDelay
}

// PollSize is pollSize, which Windows reads the size of the console with.
var PollSize = pollSize

// WatchPaste passes on what r reads and calls onPaste for each Ctrl+V, as
// it does on Linux and macOS.
func WatchPaste(r io.Reader, onPaste func()) io.Reader {
	return &pasteWatcher{r: r, onPaste: onPaste, from: -1}
}

// WatchPasteAltV does as WatchPaste, and takes Alt+V for Ctrl+V too, as it
// does on Windows.
func WatchPasteAltV(r io.Reader, onPaste func()) io.Reader {
	return &pasteWatcher{r: r, onPaste: onPaste, altV: true, from: -1}
}
