package link

import (
	"net"
	"time"
)

// HostWithTimeouts starts the host end with short timeouts for the kind of a
// stream and for a stream nobody accepts.
func HostWithTimeouts(conn net.Conn, kind, wait time.Duration) (*HostEnd, error) {
	return host(conn, kind, wait)
}

// Streams is how many streams the host end holds.
func (h *HostEnd) Streams() int { return h.session.NumStreams() }

// Greeting is what the guest end sends before anything else.
const Greeting = greeting
