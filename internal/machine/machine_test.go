package machine_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/vm"
)

// statePath is where a test keeps the state disk.
func statePath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "state.ext4")
}

// imageFolder holds the files of an image.
func imageFolder(t *testing.T, names ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}

	return dir
}

func TestPrepareBootsTheKernelAndRootDiskOfTheImage(t *testing.T) {
	// arrange
	image := imageFolder(t, "vmlinuz", "os.ext4")
	state := statePath(t)

	// act
	m, err := machine.Prepare(backend.Spec{Image: image, State: state, MemoryMiB: 1024, CPUs: 3, Shell: true})

	// assert
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(image, "vmlinuz"), m.Kernel)
	assert.Equal(t, filepath.Join(image, "os.ext4"), m.Rootfs)
	assert.Equal(t, state, m.State)
	assert.Equal(t, 1024, m.MemoryMiB)
	assert.Equal(t, 3, m.CPUs)
	assert.True(t, m.Shell)
	assert.Nil(t, m.Owner)
}

func TestImageNamesTheMissingFileAndHowToBuildIt(t *testing.T) {
	// act
	_, _, err := machine.Image(imageFolder(t, "vmlinuz"))

	// assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "os.ext4")
	assert.Contains(t, err.Error(), "just install-image")
}

func TestImageOfACompleteFolder(t *testing.T) {
	// arrange
	image := imageFolder(t, "vmlinuz", "os.ext4")

	// act
	kernel, rootfs, err := machine.Image(image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(image, "vmlinuz"), kernel)
	assert.Equal(t, filepath.Join(image, "os.ext4"), rootfs)
}

func TestPrepareSharesTheProjectAndHomeThenTheMountsByNumber(t *testing.T) {
	// arrange
	spec := backend.Spec{
		Image:   imageFolder(t, "vmlinuz", "os.ext4"),
		State:   statePath(t),
		Project: "/work/project",
		Home:    "/aibox/home",
		Mounts: []backend.Mount{
			{Host: "/h/.claude/skills", Guest: "/home/user/.claude/skills"},
			{Host: "/sdk/go", Guest: "/opt/go"},
		},
	}

	// act
	m, err := machine.Prepare(spec)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []vm.Share{
		{Tag: "project", Dir: "/work/project"},
		{Tag: "home", Dir: "/aibox/home"},
		{Tag: "mount0", Dir: "/h/.claude/skills", Guest: "/home/user/.claude/skills"},
		{Tag: "mount1", Dir: "/sdk/go", Guest: "/opt/go"},
	}, m.Shares)
}

func TestPrepareWithoutMountsSharesOnlyTheProjectAndHome(t *testing.T) {
	// act
	m, err := machine.Prepare(backend.Spec{Image: imageFolder(t, "vmlinuz", "os.ext4"), State: statePath(t), Project: "/p", Home: "/h"})

	// assert
	require.NoError(t, err)
	assert.Equal(t, []vm.Share{{Tag: "project", Dir: "/p"}, {Tag: "home", Dir: "/h"}}, m.Shares)
}

func TestPrepareCreatesTheStateDiskAsASparseFileOfTheSize(t *testing.T) {
	// arrange
	state := statePath(t)

	// act
	_, err := machine.Prepare(backend.Spec{Image: imageFolder(t, "vmlinuz", "os.ext4"), State: state, StateBytes: 1 << 30})

	// assert
	require.NoError(t, err)

	info, err := os.Stat(state)
	require.NoError(t, err)
	assert.Equal(t, int64(1<<30), info.Size())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	var stat syscall.Stat_t
	require.NoError(t, syscall.Stat(state, &stat))
	assert.Less(t, stat.Blocks*512, int64(1<<20), "the file takes up space before anything was written")
}

func TestPrepareSizesAnEmptyStateDisk(t *testing.T) {
	// arrange
	state := statePath(t)
	require.NoError(t, os.WriteFile(state, nil, 0o600))

	// act
	_, err := machine.Prepare(backend.Spec{Image: imageFolder(t, "vmlinuz", "os.ext4"), State: state, StateBytes: 1 << 30})

	// assert
	require.NoError(t, err)

	info, err := os.Stat(state)
	require.NoError(t, err)
	assert.Equal(t, int64(1<<30), info.Size())
}

func TestPrepareKeepsAnExistingStateDisk(t *testing.T) {
	// arrange
	state := statePath(t)
	require.NoError(t, os.WriteFile(state, []byte("data of the project"), 0o600))

	// act
	_, err := machine.Prepare(backend.Spec{Image: imageFolder(t, "vmlinuz", "os.ext4"), State: state, StateBytes: 1 << 30})

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(state) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "data of the project", string(content))
}

func TestPrepareCreatesNoStateDiskForAnIncompleteImage(t *testing.T) {
	// arrange
	state := statePath(t)

	// act
	_, err := machine.Prepare(backend.Spec{Image: imageFolder(t, "vmlinuz"), State: state, StateBytes: 1 << 30})

	// assert
	require.Error(t, err)
	assert.NoFileExists(t, state)
}

func TestLockStateKeepsASecondRunOff(t *testing.T) {
	// arrange
	state := statePath(t)
	require.NoError(t, os.WriteFile(state, nil, 0o600))
	first, err := machine.LockState(state)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	// act
	_, err = machine.LockState(state)

	// assert
	require.ErrorIs(t, err, machine.ErrStateBusy)
	assert.Contains(t, err.Error(), "another aibox runs this project")
}

func TestLockStateIsFreeAgainOnceTheFirstRunEnds(t *testing.T) {
	// arrange
	state := statePath(t)
	require.NoError(t, os.WriteFile(state, nil, 0o600))
	first, err := machine.LockState(state)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	// act
	second, err := machine.LockState(state)

	// assert
	require.NoError(t, err)
	assert.NoError(t, second.Close())
}
