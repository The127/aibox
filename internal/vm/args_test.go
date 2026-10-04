package vm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/vm"
)

// baseline is the kernel command line of every machine.
const baseline = "root=/dev/vda rootfstype=ext4 rw console=hvc0 quiet panic=-1 reboot=t"

func TestQEMUArgs(t *testing.T) {
	// arrange
	machine := vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/os.ext4",
		MemoryMiB: 2048,
		CPUs:      2,
		Shares: []vm.Share{
			{Tag: "project", Dir: "/home/someone/project", Socket: "/run/aibox/project.sock"},
			{Tag: "home", Dir: "/home/someone/.aibox/home", Socket: "/run/aibox/home.sock"},
		},
		GuestCID:   42,
		ConsoleLog: "/home/someone/.aibox/projects/p/console.log",
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Equal(t, []string{
		"-machine", "microvm,acpi=off,rtc=on,memory-backend=mem",
		"-enable-kvm", "-cpu", "host",
		"-smp", "2",
		"-m", "2048M",
		"-object", "memory-backend-memfd,id=mem,size=2048M,share=on",
		"-nodefaults", "-no-user-config", "-display", "none", "-no-reboot",
		"-chardev", "file,id=console,path=/home/someone/.aibox/projects/p/console.log",
		"-device", "virtio-serial-device",
		"-device", "virtconsole,chardev=console",
		"-kernel", "/images/vmlinuz",
		"-append", baseline,
		"-drive", "id=root,file=/images/os.ext4,format=raw,if=none,snapshot=on",
		"-device", "virtio-blk-device,drive=root",
		"-chardev", "socket,id=share-project,path=/run/aibox/project.sock",
		"-device", "vhost-user-fs-device,chardev=share-project,tag=project",
		"-chardev", "socket,id=share-home,path=/run/aibox/home.sock",
		"-device", "vhost-user-fs-device,chardev=share-home,tag=home",
		"-device", "vhost-vsock-device,guest-cid=42",
	}, args)
}

func TestQEMUArgsWithoutShares(t *testing.T) {
	// arrange
	machine := vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/os.ext4",
		MemoryMiB: 512,
		CPUs:      1,
		GuestCID:  3,
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, "/images/vmlinuz")

	for _, arg := range args {
		assert.NotContains(t, arg, "vhost-user-fs")
		assert.NotContains(t, arg, "id=share-")
	}
}

func TestQEMUArgsEscapesCommas(t *testing.T) {
	// arrange
	machine := vm.Machine{
		Kernel:     "/images/vmlinuz",
		Rootfs:     "/images/my,disk.ext4",
		MemoryMiB:  512,
		CPUs:       1,
		Shares:     []vm.Share{{Tag: "project", Dir: "/project", Socket: "/run/a,b/project.sock"}},
		GuestCID:   3,
		ConsoleLog: "/home/a,b/console.log",
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, "id=root,file=/images/my,,disk.ext4,format=raw,if=none,snapshot=on")
	assert.Contains(t, args, "socket,id=share-project,path=/run/a,,b/project.sock")
	assert.Contains(t, args, "file,id=console,path=/home/a,,b/console.log")
}

func TestQEMUArgsDropTheConsoleWithoutALog(t *testing.T) {
	// arrange
	machine := vm.Machine{Kernel: "/images/vmlinuz", Rootfs: "/images/os.ext4", MemoryMiB: 512, CPUs: 1, GuestCID: 3}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, "null,id=console")
}

func TestQEMUArgsWithTerminalPort(t *testing.T) {
	// arrange
	machine := vm.Machine{
		Kernel:       "/images/vmlinuz",
		Rootfs:       "/images/os.ext4",
		MemoryMiB:    512,
		CPUs:         1,
		GuestCID:     3,
		ProxyPort:    4321,
		TerminalPort: 5432,
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, baseline+" aibox.proxy=4321 aibox.terminal=5432")
}

func TestQEMUArgsEndQEMUWhenTheVMResets(t *testing.T) {
	// arrange
	machine := vm.Machine{Kernel: "/images/vmlinuz", Rootfs: "/images/os.ext4", MemoryMiB: 512, CPUs: 1, GuestCID: 3}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, "-no-reboot")
	assert.Contains(t, args, baseline)
	assert.Contains(t, baseline, "reboot=t")
}

func TestQEMUArgsWithShell(t *testing.T) {
	// arrange
	machine := vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/os.ext4",
		MemoryMiB: 512,
		CPUs:      1,
		GuestCID:  3,
		Shell:     true,
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, baseline+" aibox.shell")
}

func TestQEMUArgsWithProxyPort(t *testing.T) {
	// arrange
	machine := vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/os.ext4",
		MemoryMiB: 512,
		CPUs:      1,
		GuestCID:  3,
		Shell:     true,
		ProxyPort: 4321,
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, baseline+" aibox.shell aibox.proxy=4321")
}

func TestQEMUArgsTellTheVMWhereToMountAShare(t *testing.T) {
	// arrange
	machine := vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/os.ext4",
		MemoryMiB: 512,
		CPUs:      1,
		GuestCID:  3,
		Shares: []vm.Share{
			{Tag: "project", Dir: "/home/someone/project", Socket: "/run/aibox/project.sock"},
			{Tag: "mount0", Dir: "/opt/sdk/go", Socket: "/run/aibox/mount0.sock", Guest: "/opt/go"},
			{Tag: "mount1", Dir: "/home/someone/bin", Socket: "/run/aibox/mount1.sock", Guest: "/opt/bin"},
		},
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, baseline+" aibox.mount=mount0:/opt/go aibox.mount=mount1:/opt/bin")
}

func TestQEMUArgsTellTheVMThePath(t *testing.T) {
	// arrange
	machine := vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/os.ext4",
		MemoryMiB: 512,
		CPUs:      1,
		GuestCID:  3,
		Path:      []string{"/opt/go/bin", "/opt/bin"},
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, baseline+" aibox.path=/opt/go/bin:/opt/bin")
}

func TestVirtiofsdArgsServeAShareWithAGuestPathReadOnly(t *testing.T) {
	// arrange
	share := vm.Share{Tag: "mount0", Dir: "/opt/sdk/go", Socket: "/run/aibox/mount0.sock", Guest: "/opt/go"}

	// act
	args := share.VirtiofsdArgs(&vm.Owner{UID: 1234, GID: 100})

	// assert
	assert.Equal(t, []string{
		"--socket-path=/run/aibox/mount0.sock",
		"--shared-dir=/opt/sdk/go",
		"--readonly",
		"--cache=always",
		"--translate-uid=guest:1000:1234:1",
		"--translate-uid=host:1234:1000:1",
		"--translate-gid=guest:1000:100:1",
		"--translate-gid=host:100:1000:1",
	}, args)
}

func TestVirtiofsdArgs(t *testing.T) {
	// arrange
	share := vm.Share{Tag: "project", Dir: "/home/someone/project", Socket: "/run/aibox/project.sock"}

	// act
	args := share.VirtiofsdArgs(nil)

	// assert
	assert.Equal(t, []string{
		"--socket-path=/run/aibox/project.sock",
		"--shared-dir=/home/someone/project",
	}, args)
}

func TestVirtiofsdArgsTranslateTheOwnerToTheVMUser(t *testing.T) {
	// arrange
	share := vm.Share{Tag: "project", Dir: "/home/someone/project", Socket: "/run/aibox/project.sock"}

	// act
	args := share.VirtiofsdArgs(&vm.Owner{UID: 1234, GID: 100})

	// assert
	assert.Equal(t, []string{
		"--socket-path=/run/aibox/project.sock",
		"--shared-dir=/home/someone/project",
		"--translate-uid=guest:1000:1234:1",
		"--translate-uid=host:1234:1000:1",
		"--translate-gid=guest:1000:100:1",
		"--translate-gid=host:100:1000:1",
	}, args)
}

func TestVirtiofsdArgsTranslateRootToo(t *testing.T) {
	// arrange
	share := vm.Share{Tag: "project", Dir: "/root/project", Socket: "/run/aibox/project.sock"}

	// act
	args := share.VirtiofsdArgs(&vm.Owner{})

	// assert
	assert.Contains(t, args, "--translate-uid=guest:1000:0:1")
	assert.Contains(t, args, "--translate-gid=host:0:1000:1")
}
