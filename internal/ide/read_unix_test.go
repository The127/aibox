//go:build !windows

package ide

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A FIFO can take the place of a lock file after the folder was read, so
// read meets it without Find having seen it.
func TestReadDoesNotWaitOnAFIFO(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "1001.lock")
	require.NoError(t, syscall.Mkfifo(path, 0o600))

	done := make(chan bool, 1)

	// act
	go func() {
		_, _, ok := read(path)
		done <- ok
	}()

	// assert
	select {
	case ok := <-done:
		assert.False(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("read waits on the FIFO")
	}
}
