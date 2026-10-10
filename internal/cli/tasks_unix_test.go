//go:build !windows

package cli

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/task"
)

func TestCleanLeavesATaskWhoseLockItCannotTake(t *testing.T) {
	// arrange
	f := newTasksFixture(t)
	f.ended(t, "ended")
	f.write(t, filepath.Join("fifo", "share", task.InputBundle))
	require.NoError(t, syscall.Mkfifo(filepath.Join(f.tasks, "fifo", lockFile), 0o600))

	// act
	err := f.clean()

	// assert
	require.ErrorContains(t, err, "check the task fifo")
	assert.NoFileExists(t, filepath.Join(f.tasks, "ended", "share", task.InputBundle))
	assert.FileExists(t, filepath.Join(f.tasks, "fifo", "share", task.InputBundle))
	assert.Contains(t, f.stderr.String(), "aibox: removed the inputs of 1 task that ended\n")
}
