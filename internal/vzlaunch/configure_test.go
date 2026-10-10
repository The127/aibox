//go:build darwin && cgo

package vzlaunch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Code-Hex/vz/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/vm"
)

// testMachine is a machine whose files and folders exist, so that
// Virtualization.framework can check its configuration.
func testMachine(t *testing.T) vm.Machine {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"vmlinuz", "os.ext4", "state.ext4"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), make([]byte, 1<<20), 0o600))
	}

	return vm.Machine{
		Kernel:    filepath.Join(dir, "vmlinuz"),
		Rootfs:    filepath.Join(dir, "os.ext4"),
		State:     filepath.Join(dir, "state.ext4"),
		MemoryMiB: 512,
		CPUs:      1,
		Shares: []vm.Share{
			{Tag: "project", Dir: t.TempDir()},
			{Tag: "home", Dir: t.TempDir()},
			{Tag: "mount0", Dir: t.TempDir(), Guest: "/opt/go"},
		},
	}
}

// configured is the configuration of the machine, with the files its
// console reads from and writes to.
func configured(t *testing.T, m vm.Machine) (*vz.VirtualMachineConfiguration, error) {
	t.Helper()

	console, err := os.Create(filepath.Join(t.TempDir(), "console.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = console.Close() })

	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	t.Cleanup(func() { _ = devNull.Close() })

	return configure(m, devNull, console)
}

func TestConfigureGivesTheVMItsDisksVsockAndNoNetwork(t *testing.T) {
	// act
	config, err := configured(t, testMachine(t))

	// assert
	require.NoError(t, err)
	assert.Len(t, config.StorageDevices(), 2)
	assert.Len(t, config.SocketDevices(), 1)
	assert.Empty(t, config.NetworkDevices())
}

func TestVirtualizationFrameworkTakesTheConfiguration(t *testing.T) {
	// arrange
	config, err := configured(t, testMachine(t))
	require.NoError(t, err)

	// act
	ok, err := config.Validate()

	// assert
	if err != nil && strings.Contains(err.Error(), "entitlement") {
		t.Skip("Virtualization.framework checks a configuration only for a signed process, run with go test -exec hack/run-signed")
	}

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestTheRootDiskIsReadOnlyAndComesBeforeTheStateDisk(t *testing.T) {
	// arrange
	m := testMachine(t)

	// act
	disks := disksOf(m)

	// assert
	assert.Equal(t, []disk{{path: m.Rootfs, readOnly: true}, {path: m.State, readOnly: false}}, disks)
}

func TestASharedFolderTheGuestMountsAtAPathOfItsOwnIsReadOnly(t *testing.T) {
	// assert
	assert.False(t, readOnly(vm.Share{Tag: "project", Dir: "/p"}))
	assert.False(t, readOnly(vm.Share{Tag: "home", Dir: "/h"}))
	assert.True(t, readOnly(vm.Share{Tag: "mount0", Dir: "/sdk/go", Guest: "/opt/go"}))
}

func TestAStartThatFailedOnADiskAnotherRunHasSaysSo(t *testing.T) {
	// arrange
	state := filepath.Join(t.TempDir(), "state.ext4")
	require.NoError(t, os.WriteFile(state, nil, 0o600))
	other, err := machine.LockState(state)
	require.NoError(t, err)

	defer func() { _ = other.Close() }()

	// act
	err = startError(errors.New("the storage device attachment is invalid"), state)

	// assert
	assert.ErrorIs(t, err, machine.ErrStateBusy)
}

func TestAStartThatFailedOtherwiseKeepsItsError(t *testing.T) {
	// arrange
	state := filepath.Join(t.TempDir(), "state.ext4")
	require.NoError(t, os.WriteFile(state, nil, 0o600))
	failed := errors.New("the kernel is not valid")

	// act
	err := startError(failed, state)

	// assert
	require.ErrorIs(t, err, failed)
	assert.NotErrorIs(t, err, machine.ErrStateBusy)
	assert.ErrorContains(t, err, "start the VM")
}

func TestTheConsoleGoesToTheLogOrNowhere(t *testing.T) {
	// arrange
	log := filepath.Join(t.TempDir(), "console.log")
	require.NoError(t, os.WriteFile(log, []byte("the run before"), 0o600))

	// act
	toLog, err := openConsole(log)
	require.NoError(t, err)
	_, err = toLog.WriteString("boo")
	require.NoError(t, err)
	require.NoError(t, toLog.keep())
	_, err = toLog.WriteString("ted")
	require.NoError(t, err)
	toLog.close()

	nowhere, err := openConsole("")
	require.NoError(t, err)
	require.NoError(t, nowhere.keep())
	nowhere.close()

	// assert
	content, err := os.ReadFile(log) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Equal(t, "booted", string(content), "what the VM wrote before and after the log took the place of the old one")
	assert.Equal(t, os.DevNull, nowhere.Name())

	info, err := os.Stat(log)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the console log is the project's, as on Linux")
}

func TestARunWhoseVMDidNotStartLeavesTheConsoleLogAlone(t *testing.T) {
	// arrange
	dir := t.TempDir()
	log := filepath.Join(dir, "console.log")
	require.NoError(t, os.WriteFile(log, []byte("the running VM"), 0o600))

	// act
	refused, err := openConsole(log)
	require.NoError(t, err)
	_, err = refused.WriteString("refused")
	require.NoError(t, err)
	refused.close()

	// assert
	content, err := os.ReadFile(log) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Equal(t, "the running VM", string(content))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the log of the refused run is gone")
}

func TestTheConsoleLogOfAStartedVMStaysWhenItCannotTakeThePlaceOfTheLog(t *testing.T) {
	// arrange
	dir := t.TempDir()
	log := filepath.Join(dir, "console.log")
	// a folder that is not empty is no place a file can be renamed to
	require.NoError(t, os.MkdirAll(filepath.Join(log, "in the way"), 0o700))

	console, err := openConsole(log)
	require.NoError(t, err)
	_, err = console.WriteString("booted")
	require.NoError(t, err)

	// act
	err = console.keep()
	console.close()

	// assert
	require.Error(t, err)
	assert.ErrorContains(t, err, console.Name(), "says where the log is")

	content, err := os.ReadFile(console.Name())
	require.NoError(t, err)
	assert.Equal(t, "booted", string(content))
}

func TestConfigureRefusesAKernelThatIsNotTheReleasedOne(t *testing.T) {
	// arrange
	m := testMachine(t)
	m.KernelDigest = "the digest of another kernel"

	// act
	_, err := configured(t, m)

	// assert
	require.ErrorIs(t, err, machine.ErrKernel)
}
