package session_test

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/the127/aibox/internal/session"
)

func TestNewClientWithoutATerminalUsesADefaultSize(t *testing.T) {
	// arrange
	t.Setenv("TERM", "xterm-kitty")
	stdin, err := os.Create(filepath.Join(t.TempDir(), "stdin"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stdin.Close() })

	// act
	client, restore, err := session.NewClient(stdin, io.Discard)

	// assert
	require.NoError(t, err)
	t.Cleanup(restore)
	assert.Same(t, stdin, client.In)
	assert.Equal(t, io.Discard, client.Out)
	assert.Equal(t, "xterm-kitty", client.Term)
	assert.Equal(t, session.Size{Rows: 24, Cols: 80}, client.Size)
	assert.Nil(t, client.Resized)
}

func TestNewClientWithoutATERMFallsBackToXterm(t *testing.T) {
	// arrange
	t.Setenv("TERM", "")

	// act
	client, restore, err := session.NewClient(nil, io.Discard)

	// assert
	require.NoError(t, err)
	t.Cleanup(restore)
	assert.Equal(t, "xterm-256color", client.Term)
}

func TestNewClientWithoutStdinTypesNothing(t *testing.T) {
	// act
	client, restore, err := session.NewClient(nil, io.Discard)

	// assert
	require.NoError(t, err)
	t.Cleanup(restore)
	assert.Nil(t, client.In)
	assert.Equal(t, session.Size{Rows: 24, Cols: 80}, client.Size)
}

func echoes(t *testing.T, terminal *os.File) bool {
	t.Helper()

	termios, err := unix.IoctlGetTermios(int(terminal.Fd()), unix.TCGETS)
	require.NoError(t, err)

	return termios.Lflag&unix.ECHO != 0
}

func TestNewClientPutsTheTerminalIntoRawModeUntilRestored(t *testing.T) {
	// arrange
	pty, err := session.OpenPTY(session.Size{Rows: 50, Cols: 160})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pty.Close() })
	require.True(t, echoes(t, pty.Slave))

	// act
	client, restore, err := session.NewClient(pty.Slave, io.Discard)

	// assert
	require.NoError(t, err)
	assert.False(t, echoes(t, pty.Slave))
	assert.Equal(t, session.Size{Rows: 50, Cols: 160}, client.Size)
	assert.NotNil(t, client.Resized)

	restore()
	assert.True(t, echoes(t, pty.Slave))
}

func TestNewClientReportsSizeChangesOfTheTerminal(t *testing.T) {
	// arrange
	pty, err := session.OpenPTY(session.Size{Rows: 50, Cols: 160})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pty.Close() })

	client, restore, err := session.NewClient(pty.Slave, io.Discard)
	require.NoError(t, err)
	t.Cleanup(restore)

	// act
	require.NoError(t, pty.Resize(session.Size{Rows: 30, Cols: 100}))
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGWINCH))

	// assert
	select {
	case size := <-client.Resized:
		assert.Equal(t, session.Size{Rows: 30, Cols: 100}, size)
	case <-time.After(5 * time.Second):
		t.Fatal("no size change was reported")
	}
}
