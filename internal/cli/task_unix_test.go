//go:build !windows

package cli

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskRefusesAFIFOAsThePromptFile(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.deps.stdin = strings.NewReader("")
	require.NoError(t, syscall.Mkfifo(filepath.Join(f.cwd, "fifo"), 0o600))

	// act
	err := f.task("--file", "fifo")

	// assert
	require.ErrorContains(t, err, errNoPromptFile.Error())
	assert.False(t, f.launch.called)
}
