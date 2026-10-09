package session

import "time"

// DrainDelay is the drainDelay of the tests, shorter than the real one, so
// that the tests that wait for it run fast.
const DrainDelay = 300 * time.Millisecond

func init() {
	drainDelay = DrainDelay
}
