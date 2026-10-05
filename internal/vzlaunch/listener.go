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
	if conn == nil {
		return false
	}

	value := reflect.ValueOf(conn)

	return value.Kind() != reflect.Pointer || !value.IsNil()
}

func (l *listener) Close() error {
	l.once.Do(func() {
		close(l.closed)

		// takes the close of vz when no Accept waits, and returns at once
		// when one does and vz closed its channel behind it. A connection of
		// the guest that comes in just then is closed.
		go func() {
			if conn, err := l.Listener.Accept(); err == nil && isConn(conn) {
				_ = conn.Close()
			}
		}()

		_ = l.Listener.Close()
	})

	return nil
}

// watched is the terminal listener, which reports when the guest connected,
// when its session ended and when it connected again, so that a VM that
// never connects, stays up after its session or started over is stopped.
// The guest is not trusted, so every connection after the first is closed.
type watched struct {
	net.Listener
	connected chan struct{}
	ended     chan struct{}
	again     chan struct{}
	once      sync.Once
	againOnce sync.Once
}

func watch(inner net.Listener) *watched {
	return &watched{Listener: inner, connected: make(chan struct{}), ended: make(chan struct{}), again: make(chan struct{})}
}

func (w *watched) Accept() (net.Conn, error) {
	conn, err := w.Listener.Accept()
	if err != nil {
		return nil, err
	}

	w.once.Do(func() {
		close(w.connected)

		go w.turnAway()
	})

	return &endingConn{Conn: conn, ended: w.ended}, nil
}

// turnAway closes each connection after the first until the listener
// closes. A guest connects again after it started over.
func (w *watched) turnAway() {
	for {
		conn, err := w.Listener.Accept()
		if err != nil {
			return
		}

		_ = conn.Close()

		w.againOnce.Do(func() { close(w.again) })
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
