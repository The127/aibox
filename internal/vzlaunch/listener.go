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
