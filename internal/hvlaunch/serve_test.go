package hvlaunch

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hugelgupf/p9/fsimpl/templatefs"
	"github.com/hugelgupf/p9/p9"
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

// fedListener is a listener whose connections the test hands it.
type fedListener struct{ *fed }

func (l fedListener) Accept() (net.Conn, error) {
	conn, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}

	return conn, nil
}

func (fedListener) Addr() net.Addr { return nil }

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

// root is a share with nothing in it.
type root struct {
	p9.DefaultWalkGetAttr
	templatefs.NoopFile
}

func (root) Attach() (p9.File, error) { return root{}, nil }

func (root) GetAttr(p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	return p9.QID{Type: p9.TypeDir}, p9.AttrMask{Mode: true}, p9.Attr{Mode: p9.ModeDirectory | 0o755}, nil
}

func TestServeSharesServesEachConnectionOfTheGuest(t *testing.T) {
	t.Parallel()

	// arrange
	l := fedListener{newFed()}
	served := make(chan struct{})

	go func() {
		defer close(served)

		serveShares(l, root{})
	}()

	// act: two mounts, each over a connection of its own
	for range 2 {
		hostSide, guestSide := net.Pipe()
		l.conns <- hostSide

		client, err := p9.NewClient(guestSide)
		require.NoError(t, err)

		f, err := client.Attach("")
		require.NoError(t, err)

		qid, _, _, err := f.GetAttr(p9.AttrMaskAll)
		require.NoError(t, err)
		assert.Equal(t, p9.TypeDir, qid.Type)

		require.NoError(t, client.Close())
	}

	// assert
	require.NoError(t, l.Close())
	within(t, served, "serveShares did not end with its listener")
}
