//go:build !windows

package machine_test

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allocatedBytes is how much space the file takes up on disk.
func allocatedBytes(t *testing.T, path string) int64 {
	t.Helper()

	var stat syscall.Stat_t
	require.NoError(t, syscall.Stat(path, &stat))

	return stat.Blocks * 512
}

// assertPrivate checks that only the person can read the file.
func assertPrivate(t *testing.T, info os.FileInfo) {
	t.Helper()

	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
