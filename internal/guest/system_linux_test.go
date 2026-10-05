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

func TestListenSocketLetsOnlyTheOwnerConnect(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "link.sock")
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })

	// act
	listener, err := guest.Linux{}.ListenSocket(path)

	// assert
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestListenSocketReplacesASocketLeftFromAnEarlierStart(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "link.sock")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	// act
	listener, err := guest.Linux{}.ListenSocket(path)

	// assert
	require.NoError(t, err)
	assert.NoError(t, listener.Close())
}
