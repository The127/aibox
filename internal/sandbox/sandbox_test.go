package sandbox_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/sandbox"
)

func spec() sandbox.Spec {
	return sandbox.Spec{
		Bubblewrap: "/usr/bin/bwrap",
		Program:    "/usr/bin/qemu-system-x86_64",
		Libraries:  "/usr/lib64",
		Firmware:   "/usr/share/qemu/qboot.rom",
		KernelFD:   5,
	}
}

func TestCommandRunsTheProgramInBubblewrapWithNothingButWhatItNeeds(t *testing.T) {
	// act
	program, args := spec().Command([]string{"-m", "512M", "-kernel", sandbox.Kernel})

	// assert
	assert.Equal(t, "/usr/bin/bwrap", program)
	assert.Equal(t, []string{
		"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--ro-bind", "/usr/lib64", "/usr/lib64",
		"--symlink", "usr/lib64", "/lib64",
		"--ro-bind", "/usr/bin/qemu-system-x86_64", "/usr/bin/qemu-system-x86_64",
		"--ro-bind", "/usr/share/qemu/qboot.rom", "/usr/share/qemu/qboot.rom",
		"--ro-bind-data", "5", "/kernel",
		"--dev-bind", "/dev/null", "/dev/null",
		"--",
		"/usr/bin/qemu-system-x86_64", "-m", "512M", "-kernel", "/kernel",
	}, args)
}

func TestCommandSharesNoNetworkAndNoFolderOfTheHost(t *testing.T) {
	// act
	_, args := spec().Command(nil)

	// assert
	assert.NotContains(t, args, "--share-net")
	assert.NotContains(t, args, "--proc")
	assert.NotContains(t, args, "--dev")

	for _, arg := range args {
		assert.NotEqual(t, "/", arg)
		assert.NotEqual(t, "/home", arg)
	}
}

func TestCommandBindsTheNixStoreForAProgramFromNix(t *testing.T) {
	// arrange
	s := spec()
	s.Program = "/nix/store/abc-qemu/bin/qemu-system-x86_64"
	s.Libraries = sandbox.NixStore
	s.Firmware = "/nix/store/abc-qemu/share/qemu/qboot.rom"

	// act
	_, args := s.Command(nil)

	// assert
	assert.Equal(t, []string{"--ro-bind", "/nix/store", "/nix/store"}, args[4:7])
	assert.NotContains(t, args, "/lib64")
}
