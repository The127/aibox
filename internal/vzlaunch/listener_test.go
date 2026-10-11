//go:build darwin && cgo

package vzlaunch

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vzLike is a listener that closes as the one of vz does: Close hands its
// error to the next Accept and blocks until one takes it.
type vzLike struct {
	accepted chan net.Conn
	results  chan error
	// waiting gets a value each time an Accept starts to wait
	waiting chan struct{}
}

func newVZLike() *vzLike {
	return &vzLike{accepted: make(chan net.Conn), results: make(chan error), waiting: make(chan struct{}, 16)}
}

func (l *vzLike) Accept() (net.Conn, error) {
	l.waiting <- struct{}{}

	select {
	case conn := <-l.accepted:
		return conn, nil
	case err, ok := <-l.results:
		// vz returns its own nil connection, which is not a nil net.Conn
		var none *nilConn
		if !ok {
			return none, nil
		}

		return none, err
	}
}

// nilConn is the type of the nil connection vz returns with an error.
type nilConn struct{ net.Conn }

func (c *nilConn) Close() error { return c.Conn.Close() }

func (l *vzLike) Close() error {
	l.results <- errors.New("accept failed: listener has been closed")
	close(l.results)

	return nil
}

func (l *vzLike) Addr() net.Addr { return nil }

func within(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal(what)
	}
}

func TestClosingAListenerNobodyAcceptsOnDoesNotBlock(t *testing.T) {
	// arrange
	l := newListener(newVZLike())
	closed := make(chan struct{})

	// act
	go func() {
		_ = l.Close()

		close(closed)
	}()

	// assert
	within(t, closed, "Close blocked")
}

func TestAnAcceptWaitingWhenTheListenerClosesEndsWithErrClosed(t *testing.T) {
	// arrange
	inner := newVZLike()
	l := newListener(inner)
	ended := make(chan error, 1)

	go func() {
		_, err := l.Accept()
		ended <- err
	}()

	<-inner.waiting

	// act
	require.NoError(t, l.Close())

	// assert
	select {
	case err := <-ended:
		assert.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(3 * time.Second):
		t.Fatal("Accept went on after Close")
	}
}

func TestAcceptAfterCloseEndsWithErrClosed(t *testing.T) {
	// arrange
	l := newListener(newVZLike())
	require.NoError(t, l.Close())

	// act
	_, err := l.Accept()

	// assert
	assert.ErrorIs(t, err, net.ErrClosed)
}

// valueConn is a connection that is not a pointer.
type valueConn struct{ net.Conn }

func TestAConnectionThatIsNoPointerIsAConnection(t *testing.T) {
	// act
	var conn net.Conn = valueConn{}

	// assert
	assert.True(t, isConn(conn))
	assert.False(t, isConn(nil))
	assert.False(t, isConn((*nilConn)(nil)))
}
