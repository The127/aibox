package vm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/vm"
)

// baseline is the kernel command line of every machine.
const baseline = "root=/dev/vda rootfstype=ext4 rw console=hvc0 quiet panic=-1 reboot=t"

// files are the descriptors QEMU gets in the tests.
var files = vm.Files{KVM: 3, Vhost: 4, Kernel: "/dev/fd/5", Rootfs: 6, Console: 7, Shares: []int{8, 9}}

func machine() vm.Machine {
	return vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/os.ext4",
		MemoryMiB: 2048,
		CPUs:      2,
		Shares: []vm.Share{
			{Tag: "project", Dir: "/home/someone/project", Socket: "/run/aibox/project.sock"},
			{Tag: "home", Dir: "/home/someone/.aibox/home", Socket: "/run/aibox/home.sock"},
		},
	}
}

func TestQEMUArgs(t *testing.T) {
	// act
	args := machine().QEMUArgs(files)

	// assert
	assert.Equal(t, []string{
		"-machine", "microvm,acpi=off,rtc=on,memory-backend=mem,x-option-roms=off",
		"-add-fd", "fd=3,set=1",
		"-accel", "kvm,device=/dev/fdset/1",
		"-cpu", "host",
		"-smp", "2",
		"-m", "2048M",
		"-object", "memory-backend-memfd,id=mem,size=2048M,share=on",
		"-nodefaults", "-no-user-config", "-display", "none", "-no-reboot",
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
		"-chardev", "socket,id=console,fd=7",
		"-device", "virtio-serial-device",
		"-device", "virtconsole,chardev=console",
		"-kernel", "/dev/fd/5",
		"-append", baseline,
		"-add-fd", "fd=6,set=2",
		"-drive", "id=root,file=/dev/fdset/2,format=raw,if=none,snapshot=on",
		"-device", "virtio-blk-device,drive=root",
		"-chardev", "socket,id=share-project,fd=8",
		"-device", "vhost-user-fs-device,chardev=share-project,tag=project",
		"-chardev", "socket,id=share-home,fd=9",
		"-device", "vhost-user-fs-device,chardev=share-home,tag=home",
		"-device", "vhost-vsock-device,guest-cid=3,vhostfd=4",
	}, args)
}

func TestQEMUArgsWithoutShares(t *testing.T) {
	// arrange
	m := machine()
	m.Shares = nil

	// act
	args := m.QEMUArgs(vm.Files{KVM: 3, Vhost: 4, Kernel: "/dev/fd/5", Rootfs: 6, Console: 7})

	// assert
	for _, arg := range args {
		assert.NotContains(t, arg, "vhost-user-fs")
		assert.NotContains(t, arg, "id=share-")
	}
}

func TestQEMUArgsEndQEMUWhenTheVMResets(t *testing.T) {
	// act
	args := machine().QEMUArgs(files)

	// assert
	assert.Contains(t, args, "-no-reboot")
	assert.Contains(t, args, baseline)
	assert.Contains(t, baseline, "reboot=t")
}

func TestQEMUArgsWithShell(t *testing.T) {
	// arrange
	m := machine()
	m.Shell = true

	// act
	args := m.QEMUArgs(files)

	// assert
	assert.Contains(t, args, baseline+" aibox.shell")
}

func TestQEMUArgsWithProxyPort(t *testing.T) {
	// arrange
	m := machine()
	m.Shell = true
	m.ProxyPort = 4321

	// act
	args := m.QEMUArgs(files)

	// assert
	assert.Contains(t, args, baseline+" aibox.shell aibox.proxy=4321")
}

func TestQEMUArgsWithTerminalPort(t *testing.T) {
	// arrange
	m := machine()
	m.ProxyPort = 4321
	m.TerminalPort = 5432

	// act
	args := m.QEMUArgs(files)

	// assert
	assert.Contains(t, args, baseline+" aibox.proxy=4321 aibox.terminal=5432")
}

func TestQEMUArgsTellTheVMWhereToMountAShare(t *testing.T) {
	// arrange
	m := machine()
	m.Shares = append(m.Shares,
		vm.Share{Tag: "mount0", Dir: "/opt/sdk/go", Socket: "/run/aibox/mount0.sock", Guest: "/opt/go"},
		vm.Share{Tag: "mount1", Dir: "/home/someone/bin", Socket: "/run/aibox/mount1.sock", Guest: "/opt/bin"},
	)

	// act
	args := m.QEMUArgs(vm.Files{KVM: 3, Vhost: 4, Kernel: "/dev/fd/5", Rootfs: 6, Console: 7, Shares: []int{8, 9, 10, 11}})

	// assert
	assert.Contains(t, args, baseline+" aibox.mount=mount0:/opt/go aibox.mount=mount1:/opt/bin")
	assert.Contains(t, args, "socket,id=share-mount1,fd=11")
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
