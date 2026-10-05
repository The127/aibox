package session_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
