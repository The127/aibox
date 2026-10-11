package hvlaunch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/vhd"
)

// aState is a state disk as machine.Prepare makes it: a file of zeros.
func aState(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "state.ext4")
	require.NoError(t, os.WriteFile(path, make([]byte, 1<<20), 0o600))

	return path
}

// hyperVOpen opens the file as Hyper-V opens a disk: for its data, to
// write, sharing no write. Whether Hyper-V shares a delete is to be seen
// on Hyper-V; the test shares what the backend's unlink needs.
func hyperVOpen(t *testing.T, path string) windows.Handle {
	t.Helper()

	p, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)

	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = windows.CloseHandle(h) })

	return h
}

func TestPrepareStateMakesADynamicVHDOnceAndKeepsIt(t *testing.T) {
	t.Parallel()

	tests := map[string]func(t *testing.T, path string){
		"no disk yet": func(*testing.T, string) {},
		"an empty disk": func(t *testing.T, path string) {
			t.Helper()
			require.NoError(t, os.WriteFile(path, nil, 0o600))
		},
	}

	for name, before := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			path := filepath.Join(t.TempDir(), "state.ext4")
			before(t, path)

			// act
			first := prepareState(path, 8<<20)

			made, err := os.Stat(path)
			require.NoError(t, err)

			second := prepareState(path, 16<<20)

			// assert
			require.NoError(t, first)
			require.NoError(t, second)

			is, err := vhd.IsDynamic(path)
			require.NoError(t, err)
			assert.True(t, is)

			kept, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, made.Size(), kept.Size(), "the disk that is there stays as it is, whatever size is asked")
			assert.Less(t, kept.Size(), int64(8<<20), "and takes up space only as it fills")
		})
	}
}

func TestPrepareStateRefusesARawDiskAndLeavesIt(t *testing.T) {
	t.Parallel()

	// arrange
	path := aState(t)

	// act
	err := prepareState(path, 1<<20)

	// assert
	assert.ErrorIs(t, err, ErrNotDynamic)
	assert.ErrorContains(t, err, path)

	info, statErr := os.Stat(path)
	require.NoError(t, statErr)
	assert.Equal(t, int64(1<<20), info.Size())
}

func TestCheckNotBusyTellsADiskAVMHasOpen(t *testing.T) {
	t.Parallel()

	// arrange
	path := aState(t)
	require.NoError(t, checkNotBusy(path), "a disk no VM has open")

	hyperVOpen(t, path)

	// act
	err := checkNotBusy(path)

	// assert
	assert.ErrorIs(t, err, machine.ErrStateBusy)
}

func TestTheHandleForTheServiceLetsHyperVOpenTheDisk(t *testing.T) {
	t.Parallel()

	// arrange
	path := aState(t)

	h, err := openState(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = windows.CloseHandle(h) })

	// act: as Hyper-V does once the VM starts, with the handle still open
	p, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)

	vm, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)

	// assert
	require.NoError(t, err, "the handle for the service is in Hyper-V's way")
	assert.NoError(t, windows.CloseHandle(vm))
}

func TestUnlinkRemovesTheNameNowAndTheDataWhenTheVMLetsGo(t *testing.T) {
	t.Parallel()

	// arrange
	path := aState(t)
	vm := hyperVOpen(t, path)

	// act
	err := unlink(path)

	// assert
	require.NoError(t, err)

	_, statErr := os.Stat(path)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the name is gone while the VM has the disk open")

	var info windows.ByHandleFileInformation
	assert.NoError(t, windows.GetFileInformationByHandle(vm, &info), "the VM still has its disk")

	require.NoError(t, windows.CloseHandle(vm))
	require.NoError(t, os.WriteFile(path, nil, 0o600), "the name is free for a new file")
}
