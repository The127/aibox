//go:build linux

package guest_test

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/guest"
)

func TestLinuxWaitReturnsTheExitCode(t *testing.T) {
	// arrange
	cmd := exec.Command("sh", "-c", "exit 3")
	require.NoError(t, cmd.Start())

	// act
	pid, exitCode, err := guest.Linux{}.Wait()

	// assert
	require.NoError(t, err)
	assert.Equal(t, cmd.Process.Pid, pid)
	assert.Equal(t, 3, exitCode)
}

func TestLinuxWaitReportsASignalAsShellsDo(t *testing.T) {
	// arrange
	cmd := exec.Command("sh", "-c", "kill -TERM $$")
	require.NoError(t, cmd.Start())

	// act
	pid, exitCode, err := guest.Linux{}.Wait()

	// assert
	require.NoError(t, err)
	assert.Equal(t, cmd.Process.Pid, pid)
	assert.Equal(t, 128+15, exitCode)
}
