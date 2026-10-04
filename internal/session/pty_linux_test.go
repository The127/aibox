package session_test

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/the127/aibox/internal/session"
)

func sizeOf(t *testing.T, terminal *os.File) session.Size {
	t.Helper()

	ws, err := unix.IoctlGetWinsize(int(terminal.Fd()), unix.TIOCGWINSZ)
	require.NoError(t, err)

	return session.Size{Rows: ws.Row, Cols: ws.Col}
}

func TestOpenPTYGivesTheTerminalTheSize(t *testing.T) {
	// act
	pty, err := session.OpenPTY(session.Size{Rows: 50, Cols: 160})

	// assert
	require.NoError(t, err)
	t.Cleanup(func() { _ = pty.Close() })
	assert.Equal(t, session.Size{Rows: 50, Cols: 160}, sizeOf(t, pty.Slave))
	assert.True(t, strings.HasPrefix(pty.Slave.Name(), "/dev/pts/"), pty.Slave.Name())
}

func TestPTYResizesTheTerminal(t *testing.T) {
	// arrange
	pty, err := session.OpenPTY(session.Size{Rows: 50, Cols: 160})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pty.Close() })

	// act
	err = pty.Resize(session.Size{Rows: 30, Cols: 100})

	// assert
	require.NoError(t, err)
	assert.Equal(t, session.Size{Rows: 30, Cols: 100}, sizeOf(t, pty.Slave))
}

func TestPTYCarriesWhatIsTypedToTheTerminal(t *testing.T) {
	// arrange
	pty, err := session.OpenPTY(session.Size{Rows: 24, Cols: 80})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pty.Close() })

	// act
	_, err = io.WriteString(pty.Master, "hi\n")

	// assert
	require.NoError(t, err)

	typed := make([]byte, 3)
	_, err = io.ReadFull(pty.Slave, typed)
	require.NoError(t, err)
	assert.Equal(t, "hi\n", string(typed))
}
