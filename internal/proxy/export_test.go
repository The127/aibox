package proxy

import "time"

// SetNow makes the Log read the time from now.
func SetNow(l *Log, now func() time.Time) {
	l.now = now
}

// SetDrainTimeout makes the proxy read what a refused client still sends
// for d, until the test ends.
func SetDrainTimeout(t interface{ Cleanup(func()) }, d time.Duration) {
	before := drainTimeout
	drainTimeout = d
	t.Cleanup(func() { drainTimeout = before })
}

// ReadConnect is readConnect.
var ReadConnect = readConnect

// Connect is connect.
var Connect = connect
