package launch_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/vm"
)

// imageFolder holds the two files the QEMU backend boots.
func imageFolder(t *testing.T, names ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}

	return dir
}

func qemuBackend() launch.Backend {
	return launch.Backend{QEMU: "qemu", Virtiofsd: "virtiofsd", Owner: vm.Owner{UID: 1234, GID: 100}}
}

func TestBackendBootsTheKernelAndRootDiskOfTheImage(t *testing.T) {
	// arrange
	image := imageFolder(t, "vmlinuz", "os.ext4")

	// act
	machine, _, err := qemuBackend().Plan(backend.Spec{Image: image, State: "/state", MemoryMiB: 1024, CPUs: 3, Shell: true})

	// assert
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(image, "vmlinuz"), machine.Kernel)
	assert.Equal(t, filepath.Join(image, "os.ext4"), machine.Rootfs)
	assert.Equal(t, "/state", machine.State)
	assert.Equal(t, 1024, machine.MemoryMiB)
	assert.Equal(t, 3, machine.CPUs)
	assert.True(t, machine.Shell)
}

func TestBackendNamesTheMissingImageFileAndHowToBuildIt(t *testing.T) {
	// arrange
	image := imageFolder(t, "vmlinuz")

	// act
	_, _, err := qemuBackend().Plan(backend.Spec{Image: image})

	// assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "os.ext4")
	assert.Contains(t, err.Error(), "just install-image")
}

func TestBackendSharesTheProjectAndHomeThenTheMountsAndMapsTheOwner(t *testing.T) {
	// arrange
	image := imageFolder(t, "vmlinuz", "os.ext4")
	spec := backend.Spec{
		Image:   image,
		Project: "/work/project",
		Home:    "/aibox/home",
		Mounts: []backend.Mount{
			{Tag: "skills", Host: "/h/.claude/skills", Guest: "/home/user/.claude/skills"},
			{Tag: "mount0", Host: "/sdk/go", Guest: "/opt/go"},
		},
	}

	// act
	machine, _, err := qemuBackend().Plan(spec)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []vm.Share{
		{Tag: "project", Dir: "/work/project"},
		{Tag: "home", Dir: "/aibox/home"},
		{Tag: "skills", Dir: "/h/.claude/skills", Guest: "/home/user/.claude/skills"},
		{Tag: "mount0", Dir: "/sdk/go", Guest: "/opt/go"},
	}, machine.Shares)
	assert.Equal(t, &vm.Owner{UID: 1234, GID: 100}, machine.Owner)
}

func TestBackendWithoutMountsSharesOnlyTheProjectAndHome(t *testing.T) {
	// arrange
	image := imageFolder(t, "vmlinuz", "os.ext4")

	// act
	machine, _, err := qemuBackend().Plan(backend.Spec{Image: image, Project: "/p", Home: "/h"})

	// assert
	require.NoError(t, err)
	assert.Equal(t, []vm.Share{{Tag: "project", Dir: "/p"}, {Tag: "home", Dir: "/h"}}, machine.Shares)
}

func TestBackendPassesTheHostSideOptionsOn(t *testing.T) {
	// arrange
	image := imageFolder(t, "vmlinuz", "os.ext4")
	spec := backend.Spec{
		Image:       image,
		Unsandboxed: true,
		Env:         []string{"A=b"},
		Ports:       []uint16{22, 443},
		ConsoleLog:  "/log/console",
		Proxy:       proxy.Options{Hint: "hint"},
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	}

	// act
	_, options, err := qemuBackend().Plan(spec)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "qemu", options.QEMU)
	assert.Equal(t, "virtiofsd", options.Virtiofsd)
	assert.True(t, options.NoSandbox)
	assert.Equal(t, []string{"A=b"}, options.Env)
	assert.Equal(t, []uint16{22, 443}, options.Ports)
	assert.Equal(t, "/log/console", options.ConsoleLog)
	assert.Equal(t, "hint", options.Proxy.Hint)
	assert.Same(t, os.Stdin, options.Stdin)
	assert.Same(t, os.Stdout, options.Stdout)
	assert.Same(t, os.Stderr, options.Stderr)
}

func TestNewBackendRunsTheQEMUAndVirtiofsdOfAnX86Host(t *testing.T) {
	// act
	b := launch.NewBackend()

	// assert
	assert.Equal(t, "qemu-system-x86_64", b.QEMU)
	assert.Equal(t, "/usr/libexec/virtiofsd", b.Virtiofsd)
	assert.Equal(t, vm.Owner{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}, b.Owner) //nolint:gosec // never negative on Linux
}

func TestBackendChecksThatTheImageHoldsTheKernelAndRootDisk(t *testing.T) {
	// arrange
	complete := imageFolder(t, "vmlinuz", "os.ext4")
	incomplete := imageFolder(t, "os.ext4")

	// act
	ok := qemuBackend().CheckImage(complete)
	missing := qemuBackend().CheckImage(incomplete)

	// assert
	require.NoError(t, ok)
	require.Error(t, missing)
	assert.Contains(t, missing.Error(), "vmlinuz")
}
