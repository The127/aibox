package host_test

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/host"
)

// fed is a listener whose connections the test hands it.
type fed struct {
	conns chan net.Conn
}

func newFed() *fed { return &fed{conns: make(chan net.Conn)} }

func (l *fed) Accept() (net.Conn, error) {
	conn, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}

	return conn, nil
}

func (l *fed) Close() error {
	close(l.conns)

	return nil
}

func (l *fed) Addr() net.Addr { return nil }

func within(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal(what)
	}
}

func TestAWatchedTerminalSaysWhenItConnectedAndWhenItEnded(t *testing.T) {
	t.Parallel()

	// arrange
	inner := newFed()
	w := host.Watch(inner)
	hostSide, guestSide := net.Pipe()

	t.Cleanup(func() { _ = guestSide.Close() })
	t.Cleanup(func() { _ = inner.Close() })

	go func() { inner.conns <- hostSide }()

	// act
	conn, err := w.Accept()
	require.NoError(t, err)
	within(t, w.Connected, "the connection was not reported")

	select {
	case <-w.Ended:
		t.Fatal("the session was reported over while it runs")
	default:
	}

	require.NoError(t, conn.Close())

	// assert
	within(t, w.Ended, "the end of the session was not reported")
}

func TestATerminalThatConnectsAgainIsReportedAndEachExtraConnectionClosed(t *testing.T) {
	t.Parallel()

	// arrange
	inner := newFed()
	w := host.Watch(inner)
	first, firstGuest := net.Pipe()

	t.Cleanup(func() { _ = firstGuest.Close() })
	t.Cleanup(func() { _ = inner.Close() })

	go func() { inner.conns <- first }()

	conn, err := w.Accept()
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	// act
	var extras []net.Conn

	for range 3 {
		hostSide, guest := net.Pipe()
		extras = append(extras, guest)
		inner.conns <- hostSide
	}

	// assert
	within(t, w.Again, "a second connection was not reported")

	for _, guest := range extras {
		read := make(chan error, 1)

		go func() {
			_, err := guest.Read(make([]byte, 1))
			read <- err
		}()

		select {
		case err := <-read:
			assert.ErrorIs(t, err, io.EOF)
		case <-time.After(3 * time.Second):
			t.Fatal("an extra connection was left open")
		}
	}
}
