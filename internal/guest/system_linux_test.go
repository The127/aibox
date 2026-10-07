//go:build linux

package guest_test

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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

func TestStartAsksTheKernelToStartTheCommandInTheCgroup(t *testing.T) {
	// arrange
	cgroup := filepath.Join(t.TempDir(), "not-a-cgroup")
	require.NoError(t, os.Mkdir(cgroup, 0o700))
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{}

	// act
	_, err := guest.Linux{}.Start(cmd, cgroup)

	// assert
	// a plain folder refuses the process, which shows the kernel was asked
	require.Error(t, err)
	assert.True(t, cmd.SysProcAttr.UseCgroupFD)
}

func TestBlankIsTrueForADiskWithoutAFileSystem(t *testing.T) {
	// arrange
	disk := filepath.Join(t.TempDir(), "disk")
	require.NoError(t, os.WriteFile(disk, make([]byte, 4096), 0o600))

	// act
	blank, err := guest.Linux{}.Blank(disk)

	// assert
	require.NoError(t, err)
	assert.True(t, blank)
}

func TestBlankIsFalseForAnExt4Disk(t *testing.T) {
	// arrange
	disk := filepath.Join(t.TempDir(), "disk")
	content := make([]byte, 4096)
	binary.LittleEndian.PutUint16(content[1024+56:], 0xEF53)
	require.NoError(t, os.WriteFile(disk, content, 0o600))

	// act
	blank, err := guest.Linux{}.Blank(disk)

	// assert
	require.NoError(t, err)
	assert.False(t, blank)
}

func TestBlankFailsForADiskWithDataButNoFileSystem(t *testing.T) {
	// arrange
	disk := filepath.Join(t.TempDir(), "disk")
	content := make([]byte, 4096)
	content[1024+7] = 1 // in the superblock, away from the magic
	require.NoError(t, os.WriteFile(disk, content, 0o600))

	// act
	blank, err := guest.Linux{}.Blank(disk)

	// assert
	require.ErrorIs(t, err, guest.ErrDamaged)
	assert.False(t, blank)
}

func TestBlankFailsForADiskTooSmallForASuperblock(t *testing.T) {
	// arrange
	disk := filepath.Join(t.TempDir(), "disk")
	require.NoError(t, os.WriteFile(disk, make([]byte, 100), 0o600))

	// act
	_, err := guest.Linux{}.Blank(disk)

	// assert
	require.Error(t, err)
}

func TestBlankFailsForAMissingDisk(t *testing.T) {
	// act
	_, err := guest.Linux{}.Blank(filepath.Join(t.TempDir(), "gone"))

	// assert
	require.Error(t, err)
}

func TestKillEndsEveryProcessOfTheCgroup(t *testing.T) {
	// arrange
	cgroup := filepath.Join("/sys/fs/cgroup", "aibox-test-"+filepath.Base(t.TempDir()))
	if err := os.Mkdir(cgroup, 0o750); err != nil {
		t.Skipf("no cgroup to test in: %v", err)
	}

	t.Cleanup(func() { _ = os.Remove(cgroup) })

	cmd := exec.Command("sh", "-c", "sleep 100 & sleep 100")
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	_, err := guest.Linux{}.Start(cmd, cgroup)
	require.NoError(t, err)

	// act
	err = guest.Linux{}.Kill(cgroup)

	// assert
	require.NoError(t, err)

	procs, err := os.ReadFile(filepath.Join(cgroup, "cgroup.procs")) //nolint:gosec // the cgroup is the test's own
	require.NoError(t, err)
	assert.Empty(t, string(procs))

	var exit *exec.ExitError
	require.ErrorAs(t, cmd.Wait(), &exit)
	assert.Equal(t, syscall.SIGKILL, exit.Sys().(syscall.WaitStatus).Signal()) //nolint:forcetypeassert // Sys is a WaitStatus on Linux
}

func TestKillFailsForAFolderThatIsNotACgroup(t *testing.T) {
	// act
	err := guest.Linux{}.Kill(t.TempDir())

	// assert
	assert.Error(t, err)
}
