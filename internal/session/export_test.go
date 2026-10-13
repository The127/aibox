package session

import (
	"io"
	"sync/atomic"
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

// WatchPaste passes on what r reads and calls onPaste for each Ctrl+V,
// which it passes on.
func WatchPaste(r io.Reader, onPaste func()) io.Reader {
	return watchPaste(r, func() []byte { onPaste(); return nil }, false)
}

// WatchPasteAltV does as WatchPaste, and takes Alt+V for Ctrl+V too.
func WatchPasteAltV(r io.Reader, onPaste func()) io.Reader {
	return watchPaste(r, func() []byte { onPaste(); return nil }, true)
}

// WatchPasteWith passes on what r reads with each paste key replaced by
// what onPaste returns, or passed on for nil.
func WatchPasteWith(r io.Reader, onPaste func() []byte, altV bool) io.Reader {
	return watchPaste(r, onPaste, altV)
}

// Pasted is pasted.
var Pasted = pasted

// WatchMode passes on what is written to w, and tells whether it last
// turned bracketed paste on.
func WatchMode(w io.Writer) (io.Writer, func() bool) {
	on := &atomic.Bool{}

	return &modeWatcher{w: w, on: on}, on.Load
}
