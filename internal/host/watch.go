package host

import (
	"net"
	"sync"
)

// Watched is the terminal listener of a VM the backend owns, which reports
// when the guest connected, when its session ended and when it connected
// again, so that the backend stops a VM that never connects, stays up
// after its session or started over. The guest is not trusted, so every
// connection after the first is closed. Watch makes one, and each channel
// closes as what it stands for happens.
type Watched struct {
	net.Listener
	Connected chan struct{}
	Ended     chan struct{}
	Again     chan struct{}
	once      sync.Once
	againOnce sync.Once
}

// Watch watches the terminal listener.
func Watch(inner net.Listener) *Watched {
	return &Watched{Listener: inner, Connected: make(chan struct{}), Ended: make(chan struct{}), Again: make(chan struct{})}
}

// Accept accepts the first connection, and turns away the rest.
func (w *Watched) Accept() (net.Conn, error) {
	conn, err := w.Listener.Accept()
	if err != nil {
		return nil, err
	}

	w.once.Do(func() {
		close(w.Connected)

		go w.turnAway()
	})

	return &endingConn{Conn: conn, ended: w.Ended}, nil
}

// turnAway closes each connection after the first until the listener
// closes. A guest connects again after it started over.
func (w *Watched) turnAway() {
	for {
		conn, err := w.Listener.Accept()
		if err != nil {
			return
		}

		_ = conn.Close()

		w.againOnce.Do(func() { close(w.Again) })
	}
}

// endingConn reports its close, which is the end of the session.
type endingConn struct {
	net.Conn
	ended chan struct{}
	once  sync.Once
}

func (c *endingConn) Close() error {
	err := c.Conn.Close()

	c.once.Do(func() { close(c.ended) })

	return err
}
