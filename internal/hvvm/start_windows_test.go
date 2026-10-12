package hvvm

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/appcontainer"
)

func TestMain(m *testing.M) {
	// Start runs the program with the command of the helper, which is the
	// test binary here
	if len(os.Args) > 1 && os.Args[1] == Command {
		if err := serve(os.Args[2:], idle); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		os.Exit(0)
	}

	os.Exit(m.Run())
}

// idle listens on no socket: a guest never connects in this test.
func idle(guid.GUID, uint32) (net.Listener, error) {
	return &idleListener{closed: make(chan struct{})}, nil
}

type idleListener struct {
	closed chan struct{}
	once   sync.Once
}

func (l *idleListener) Accept() (net.Conn, error) {
	<-l.closed

	return nil, net.ErrClosed
}

func (l *idleListener) Close() error {
	l.once.Do(func() { close(l.closed) })

	return nil
}

func (l *idleListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestStartRunsTheHelperInTheContainerUntilAiboxEndsIt(t *testing.T) {
	// arrange
	id, err := guid.NewV4()
	require.NoError(t, err)

	name := "aibox.test." + id.String()

	sid, err := appcontainer.CreateProfile(name)
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, appcontainer.DeleteProfile(name)) })

	job, err := appcontainer.NewJob()
	require.NoError(t, err)

	t.Cleanup(func() { _ = job.Close() })

	self, err := os.Executable()
	require.NoError(t, err)

	// a writer that is no file, which reaches the helper as a pipe
	var stderr bytes.Buffer

	// act
	h, err := Start(t.Context(), Spec{Self: self, Job: job, Container: sid, Stderr: &stderr}, Config{ProxyPort: 1025, AllowList: []string{"example.com:443"}})
	require.NoError(t, err, "the helper serves")

	// assert
	select {
	case <-h.Done():
		t.Fatal("the helper ended by itself")
	default:
	}

	require.NoError(t, h.Close())
	assert.Empty(t, stderr.String())
}
