//go:build darwin && cgo

package vzlaunch

import (
	"net"
	"reflect"
	"sync"
)

// listener is a vsock listener of vz that closes without blocking. vz hands
// its close to the next Accept and blocks until one takes it, which never
// happens for the terminal, accepted once. Its own error for a closed
// listener is not net.ErrClosed either, which the proxy and the terminal
// take as the end of the VM.
type listener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func newListener(inner net.Listener) *listener {
	return &listener{Listener: inner, closed: make(chan struct{})}
}

func (l *listener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()

	select {
	case <-l.closed:
		if isConn(conn) {
			_ = conn.Close()
		}

		return nil, net.ErrClosed
	default:
	}

	if err != nil {
		return nil, err
	}

	return conn, nil
}

// isConn says whether the connection is one. vz returns a nil connection of
// its own type with an error, and once closed without one, and that is not a
// nil net.Conn.
func isConn(conn net.Conn) bool {
	return conn != nil && !reflect.ValueOf(conn).IsNil()
}

func (l *listener) Close() error {
	l.once.Do(func() {
		close(l.closed)

		// takes the close of vz when no Accept waits, and returns at once
		// when one does and vz closed its channel behind it
		go func() { _, _ = l.Listener.Accept() }()

		_ = l.Listener.Close()
	})

	return nil
}

// watched is the terminal listener, which reports when the guest connected
// and when its session ended, so that a VM that never connects or that
// stays up after its session is stopped.
type watched struct {
	net.Listener
	connected chan struct{}
	ended     chan struct{}
	once      sync.Once
}

func watch(inner net.Listener) *watched {
	return &watched{Listener: inner, connected: make(chan struct{}), ended: make(chan struct{})}
}

func (w *watched) Accept() (net.Conn, error) {
	conn, err := w.Listener.Accept()
	if err != nil {
		return nil, err
	}

	w.once.Do(func() { close(w.connected) })

	return &endingConn{Conn: conn, ended: w.ended}, nil
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
