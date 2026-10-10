package filelock_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/filelock"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // the path is in the temp folder of the test
	require.NoError(t, err)

	t.Cleanup(func() { _ = file.Close() })

	return file
}

func TestLock(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "lock")

	first := open(t, path)
	require.NoError(t, filelock.Lock(first, false))

	// the lock is the file's, so a second open of it waits
	second := open(t, path)
	assert.ErrorIs(t, filelock.Lock(second, false), filelock.ErrLocked)

	// and gets it once the first is closed
	require.NoError(t, first.Close())
	assert.NoError(t, filelock.Lock(second, false))
}

func TestLockWaits(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "lock")

	first := open(t, path)
	require.NoError(t, filelock.Lock(first, true))

	locked := make(chan error, 1)

	go func() { locked <- filelock.Lock(open(t, path), true) }()

	require.NoError(t, first.Close())
	assert.NoError(t, <-locked)
}
