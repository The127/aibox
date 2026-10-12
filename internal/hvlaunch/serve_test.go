package hvlaunch

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fed is a console pipe whose connection the test hands it.
type fed struct {
	conns chan net.Conn
	once  sync.Once
}

func newFed() *fed { return &fed{conns: make(chan net.Conn)} }

func (c *fed) Listen() error { return nil }

func (c *fed) Connect() (io.ReadCloser, error) {
	conn, ok := <-c.conns
	if !ok {
		return nil, net.ErrClosed
	}

	return conn, nil
}

func (c *fed) Close() error {
	c.once.Do(func() { close(c.conns) })

	return nil
}

func within(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal(what)
	}
}

func TestServeConsoleLogsTheConsoleUntilTheVMIsGone(t *testing.T) {
	t.Parallel()

	// arrange
	l := newFed()
	t.Cleanup(func() { _ = l.Close() })

	var log bytes.Buffer
	gone := serveConsole(l, &log)

	hyperV, guest := net.Pipe()
	l.conns <- hyperV

	// act
	_, err := guest.Write([]byte("[    0.000000] Linux version 6.1\n"))
	require.NoError(t, err)

	select {
	case <-gone:
		t.Fatal("the VM was taken for gone while it writes")
	default:
	}

	require.NoError(t, guest.Close())

	// assert
	within(t, gone, "the end of the console was not reported")
	assert.Equal(t, "[    0.000000] Linux version 6.1\n", log.String())
}

func TestServeConsoleReportsAPipeThatClosedBeforeTheVMConnected(t *testing.T) {
	t.Parallel()

	// arrange
	l := newFed()
	gone := serveConsole(l, &bytes.Buffer{})

	// act
	require.NoError(t, l.Close())

	// assert
	within(t, gone, "the close was not reported")
}
