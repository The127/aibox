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
		Libraries:  []string{"/lib64/ld-linux-x86-64.so.2", "/usr/lib/libc.so.6", "/usr/lib/libz.so.1"},
		Firmware:   []string{"/usr/share/seabios/bios-microvm.bin", "/usr/share/qemu/linuxboot_dma.bin"},
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
		"--ro-bind", "/lib64/ld-linux-x86-64.so.2", "/lib64/ld-linux-x86-64.so.2",
		"--ro-bind", "/usr/lib/libc.so.6", "/usr/lib/libc.so.6",
		"--ro-bind", "/usr/lib/libz.so.1", "/usr/lib/libz.so.1",
		"--setenv", "LD_LIBRARY_PATH", "/lib64:/usr/lib",
		"--ro-bind", "/usr/bin/qemu-system-x86_64", "/usr/bin/qemu-system-x86_64",
		"--ro-bind", "/usr/share/seabios/bios-microvm.bin", "/usr/share/seabios/bios-microvm.bin",
		"--ro-bind", "/usr/share/qemu/linuxboot_dma.bin", "/usr/share/qemu/linuxboot_dma.bin",
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
