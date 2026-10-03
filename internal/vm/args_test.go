package vm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/vm"
)

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
		GuestCID: 42,
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Equal(t, []string{
		"-machine", "microvm,acpi=on,rtc=on,memory-backend=mem",
		"-enable-kvm", "-cpu", "host",
		"-smp", "2",
		"-m", "2048M",
		"-object", "memory-backend-memfd,id=mem,size=2048M,share=on",
		"-nodefaults", "-no-user-config", "-nographic", "-no-reboot",
		"-serial", "mon:stdio",
		"-kernel", "/images/vmlinuz",
		"-append", "root=/dev/vda rootfstype=ext4 rw console=ttyS0 quiet panic=-1",
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
	assert.NotContains(t, args, "-chardev")
}

func TestQEMUArgsEscapesCommas(t *testing.T) {
	// arrange
	machine := vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/my,disk.ext4",
		MemoryMiB: 512,
		CPUs:      1,
		Shares:    []vm.Share{{Tag: "project", Dir: "/project", Socket: "/run/a,b/project.sock"}},
		GuestCID:  3,
	}

	// act
	args := machine.QEMUArgs()

	// assert
	assert.Contains(t, args, "id=root,file=/images/my,,disk.ext4,format=raw,if=none,snapshot=on")
	assert.Contains(t, args, "socket,id=share-project,path=/run/a,,b/project.sock")
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
	assert.Contains(t, args, "root=/dev/vda rootfstype=ext4 rw console=ttyS0 quiet panic=-1 aibox.shell")
}

func TestVirtiofsdArgs(t *testing.T) {
	// arrange
	share := vm.Share{Tag: "project", Dir: "/home/someone/project", Socket: "/run/aibox/project.sock"}

	// act
	args := share.VirtiofsdArgs()

	// assert
	assert.Equal(t, []string{
		"--socket-path=/run/aibox/project.sock",
		"--shared-dir=/home/someone/project",
	}, args)
}
