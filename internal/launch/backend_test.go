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

// statePath is where a test keeps the state disk.
func statePath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "state.ext4")
}

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

func TestBackendMapsTheOwnerInTheShares(t *testing.T) {
	// arrange
	image := imageFolder(t, "vmlinuz", "os.ext4")

	// act
	m, _, err := qemuBackend().Prepare(backend.Spec{Image: image, State: statePath(t), Project: "/p", Home: "/h"})

	// assert
	require.NoError(t, err)
	assert.Equal(t, &vm.Owner{UID: 1234, GID: 100}, m.Owner)
	assert.Equal(t, filepath.Join(image, "vmlinuz"), m.Kernel)
}

func TestBackendPassesTheHostSideOptionsOn(t *testing.T) {
	// arrange
	image := imageFolder(t, "vmlinuz", "os.ext4")
	spec := backend.Spec{
		Image:       image,
		State:       statePath(t),
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
	_, options, err := qemuBackend().Prepare(spec)

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
