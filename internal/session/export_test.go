package session

import "time"

// DrainDelay is the drainDelay of the tests, shorter than the real one, so
// that the tests that wait for it run fast.
const DrainDelay = 300 * time.Millisecond

func init() {
	drainDelay = DrainDelay
}

// PollSize is pollSize, which Windows reads the size of the console with.
var PollSize = pollSize

// WatchPaste passes on what r reads and calls onPaste for each Ctrl+V.
var WatchPaste = watchPaste
