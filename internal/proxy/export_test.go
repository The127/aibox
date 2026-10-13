package proxy

import "time"

// SetNow makes the Log read the time from now.
func SetNow(l *Log, now func() time.Time) {
	l.now = now
}

// ReadConnect is readConnect.
var ReadConnect = readConnect
