package tunnel_test

import (
	"context"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/tunnel"
)

const timeout = 5 * time.Second

// pair returns the two ends of a TCP connection.
func pair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = listener.Close() }()

	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)

	server, err := listener.Accept()
	require.NoError(t, err)

	for _, conn := range []net.Conn{client, server} {
		require.NoError(t, conn.SetDeadline(time.Now().Add(timeout)))
		t.Cleanup(func() { _ = conn.Close() })
	}

	return client.(*net.TCPConn), server.(*net.TCPConn)
}

// echo answers each connection with everything it read.
func echo(_ context.Context, conn net.Conn) {
	received, _ := io.ReadAll(conn)
	_, _ = conn.Write(received)
}

// start runs Serve and returns the address and a function that stops it
// and returns the error of Serve.
func start(t *testing.T, listener net.Listener, handle func(context.Context, net.Conn)) func() error {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- tunnel.Serve(ctx, listener, handle) }()

	return func() error {
		cancel()

		select {
		case err := <-done:
			return err
		case <-time.After(timeout):
			t.Fatal("Serve did not return")

			return nil
		}
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	return listener
}

func dial(t *testing.T, listener net.Listener) net.Conn {
	t.Helper()

	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, conn.SetDeadline(time.Now().Add(timeout)))
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

func TestJoinCopiesBothWaysAndPassesTheHalfCloseOn(t *testing.T) {
	// arrange
	left, leftEnd := pair(t)
	rightEnd, right := pair(t)
	done := make(chan struct{})

	go func() {
		tunnel.Join(leftEnd, rightEnd)
		close(done)
	}()

	// act
	_, err := io.WriteString(left, "from the left")
	require.NoError(t, err)
	require.NoError(t, left.CloseWrite())

	fromLeft, err := io.ReadAll(right)
	require.NoError(t, err)

	_, err = io.WriteString(right, "from the right")
	require.NoError(t, err)
	require.NoError(t, right.CloseWrite())

	fromRight, err := io.ReadAll(left)
	require.NoError(t, err)

	// assert
	assert.Equal(t, "from the left", string(fromLeft))
	assert.Equal(t, "from the right", string(fromRight))

	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("Join did not return after both sides closed")
	}
}

func TestJoinClosesASideThatHasNoHalfClose(t *testing.T) {
	// arrange
	left, leftEnd := pair(t)
	rightEnd, right := net.Pipe()

	go tunnel.Join(leftEnd, rightEnd)

	// act
	require.NoError(t, left.CloseWrite())

	_, err := right.Read(make([]byte, 1))

	// assert
	assert.ErrorIs(t, err, io.EOF)
}

func TestServeHandlesEachConnection(t *testing.T) {
	// arrange
	listener := listen(t)
	stop := start(t, listener, echo)
	t.Cleanup(func() { assert.NoError(t, stop()) })

	// act
	var replies []string

	for _, message := range []string{"one", "two"} {
		conn := dial(t, listener)
		_, err := io.WriteString(conn, message)
		require.NoError(t, err)
		require.NoError(t, conn.(*net.TCPConn).CloseWrite())

		reply, err := io.ReadAll(conn)
		require.NoError(t, err)

		replies = append(replies, string(reply))
	}

	// assert
	assert.Equal(t, []string{"one", "two"}, replies)
}

func TestServeClosesTheConnectionsWhenTheContextEnds(t *testing.T) {
	// arrange
	listener := listen(t)
	handling := make(chan struct{})
	stop := start(t, listener, func(_ context.Context, conn net.Conn) {
		close(handling)
		// a handler blocked on its connection, as a tunnel is
		_, _ = conn.Read(make([]byte, 1))
	})
	conn := dial(t, listener)
	<-handling

	// act
	err := stop()

	// assert
	require.NoError(t, err)

	_, err = conn.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

// failingOnce is a listener whose first Accept fails with the error.
type failingOnce struct {
	net.Listener
	err    error
	failed bool
}

func (l *failingOnce) Accept() (net.Conn, error) {
	if !l.failed {
		l.failed = true

		return nil, l.err
	}

	return l.Listener.Accept()
}

func TestServeKeepsGoingAfterATransientAcceptError(t *testing.T) {
	// arrange
	listener := listen(t)
	stop := start(t, &failingOnce{Listener: listener, err: syscall.EMFILE}, echo)
	t.Cleanup(func() { assert.NoError(t, stop()) })

	// act
	conn := dial(t, listener)
	_, err := io.WriteString(conn, "still here")
	require.NoError(t, err)
	require.NoError(t, conn.(*net.TCPConn).CloseWrite())

	reply, err := io.ReadAll(conn)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "still here", string(reply))
}

func TestServeReturnsAnotherAcceptError(t *testing.T) {
	// arrange
	listener := listen(t)
	done := make(chan error, 1)

	// act
	go func() {
		done <- tunnel.Serve(context.Background(), &failingOnce{Listener: listener, err: syscall.EINVAL}, echo)
	}()

	// assert
	select {
	case err := <-done:
		assert.ErrorIs(t, err, syscall.EINVAL)
	case <-time.After(timeout):
		t.Fatal("Serve did not return")
	}
}
