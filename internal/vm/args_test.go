package vm_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/vm"
)

// baseline is the kernel command line of every machine.
const baseline = "root=/dev/vda rootfstype=ext4 ro console=hvc0 quiet panic=-1 reboot=t"

// files are the descriptors QEMU gets in the tests.
var files = vm.Files{KVM: 3, Vhost: 4, Kernel: "/dev/fd/5", Rootfs: 6, StateRead: 7, StateWrite: 8, Shares: []int{9, 10}, Console: 11}

func machine() vm.Machine {
	return vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/os.ext4",
		State:     "/home/someone/.aibox/projects/p/state.ext4",
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
		"-machine", "microvm,rtc=on,memory-backend=mem",
		"-add-fd", "fd=3,set=1",
		"-accel", "kvm,device=/dev/fdset/1",
		"-cpu", "host",
		"-smp", "2",
		"-m", "2048M",
		"-object", "memory-backend-memfd,id=mem,size=2048M,share=on",
		"-nodefaults", "-no-user-config", "-display", "none", "-no-reboot",
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
		"-chardev", "socket,id=console,fd=11",
		"-device", "virtio-serial-device",
		"-device", "virtconsole,chardev=console",
		"-kernel", "/dev/fd/5",
		"-append", baseline,
		"-add-fd", "fd=6,set=2",
		"-drive", "id=root,file=/dev/fdset/2,format=raw,if=none,read-only=on",
		"-device", "virtio-blk-device,drive=root",
		"-add-fd", "fd=7,set=3",
		"-add-fd", "fd=8,set=3",
		"-drive", "id=state,file=/dev/fdset/3,format=raw,if=none",
		"-device", "virtio-blk-device,drive=state",
		"-chardev", "socket,id=share-project,fd=9",
		"-device", "vhost-user-fs-device,chardev=share-project,tag=project",
		"-chardev", "socket,id=share-home,fd=10",
		"-device", "vhost-user-fs-device,chardev=share-home,tag=home",
		"-device", "vhost-vsock-device,guest-cid=3,vhostfd=4",
	}, args)
}

func TestQEMUArgsPutThePortOnTheSerialDeviceInsteadOfVsock(t *testing.T) {
	// arrange
	withPort := files
	withPort.Vhost = 0
	withPort.Port = 12

	// act
	args := machine().QEMUArgs(withPort)

	// assert
	assert.Equal(t, []string{"-chardev", "socket,id=port,fd=12", "-device", "virtserialport,chardev=port,name=aibox"}, args[len(args)-4:])
	serialDevices := 0

	for _, arg := range args {
		assert.NotContains(t, arg, "vsock")

		if arg == "virtio-serial-device" {
			serialDevices++
		}
	}

	assert.Equal(t, 1, serialDevices, "the port shares the device of the console")
}

func TestQEMUArgsFillTheVirtioSlotsWithTheMostShares(t *testing.T) {
	// arrange
	m := machine()
	numbers := files

	for i := len(m.Shares); i < vm.MaxShares; i++ {
		m.Shares = append(m.Shares, vm.Share{Tag: "mount" + strconv.Itoa(i)})
		numbers.Shares = append(numbers.Shares, 20+i)
	}

	// act
	args := m.QEMUArgs(numbers)

	// assert
	devices := 0

	for i, arg := range args {
		if arg == "-device" && strings.HasSuffix(strings.Split(args[i+1], ",")[0], "-device") {
			devices++
		}
	}

	assert.Equal(t, 24, devices)
}

func TestQEMUArgsWithoutShares(t *testing.T) {
	// arrange
	m := machine()
	m.Shares = nil

	// act
	args := m.QEMUArgs(vm.Files{KVM: 3, Vhost: 4, Kernel: "/dev/fd/5", Rootfs: 6, StateRead: 7, StateWrite: 8, Console: 9})

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
	args := m.QEMUArgs(vm.Files{KVM: 3, Vhost: 4, Kernel: "/dev/fd/5", Rootfs: 6, StateRead: 7, StateWrite: 8, Shares: []int{9, 10, 11, 12}, Console: 13})

	// assert
	assert.Contains(t, args, baseline+" aibox.mount=mount0:/opt/go aibox.mount=mount1:/opt/bin")
	assert.Contains(t, args, "socket,id=share-mount1,fd=12")
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

func TestGuestWordsTellTheGuestItsSettingsPortsAndMounts(t *testing.T) {
	// arrange
	m := machine()
	m.Shell = true
	m.Resume = "0b9d3c4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e"
	m.Port = true
	m.Mise = true
	m.ProxyPort = 1025
	m.TerminalPort = 1024
	m.Shares = append(m.Shares, vm.Share{Tag: "mount0", Dir: "/sdk/go", Guest: "/opt/go"})
	m.Loopback = []uint16{3000, 64422}

	// act
	words := m.GuestWords()

	// assert
	assert.Equal(t, []string{"aibox.shell", "aibox.resume=0b9d3c4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e", "aibox.port", "aibox.mise", "aibox.proxy=1025", "aibox.terminal=1024", "aibox.mount=mount0:/opt/go", "aibox.loopback=3000,64422"}, words)
}

func TestCheckSessionIDTakesALowercaseUUID(t *testing.T) {
	// act
	err := vm.CheckSessionID("0b9d3c4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e")

	// assert
	assert.NoError(t, err)
}

func TestCheckSessionIDRejectsAnythingElse(t *testing.T) {
	for _, id := range []string{"", "x", "0B9D3C4E-5F6A-4B7C-8D9E-0F1A2B3C4D5E", "0b9d3c4e5f6a4b7c8d9e0f1a2b3c4d5e", "0b9d3c4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e aibox.shell", "0b9d3c4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e\n"} {
		t.Run(id, func(t *testing.T) {
			// act
			err := vm.CheckSessionID(id)

			// assert
			assert.ErrorIs(t, err, vm.ErrBadSessionID)
		})
	}
}

func TestGuestWordsOfAPlainMachineAreNone(t *testing.T) {
	// act
	words := machine().GuestWords()

	// assert
	assert.Empty(t, words)
}

func TestCmdlineStartsWithWhatTheVMMNeedsAndEndsWithTheWordsOfTheGuest(t *testing.T) {
	// arrange
	m := machine()
	m.Shell = true

	// act
	line := m.Cmdline(vm.RootCmdline + " panic=1")

	// assert
	assert.Equal(t, "root=/dev/vda rootfstype=ext4 ro console=hvc0 quiet panic=1 aibox.shell", line)
}

func TestGuestWordsTellTheGuestItRunsOnHyperV(t *testing.T) {
	// arrange
	m := machine()
	m.Task = true
	m.HyperV = true

	// act
	words := m.GuestWords()

	// assert
	assert.Equal(t, []string{"aibox.task", "aibox.hyperv"}, words)
}

func TestGuestWordsTellTheGuestToRunATask(t *testing.T) {
	// arrange
	m := machine()
	m.Task = true
	m.Shares = []vm.Share{{Tag: "task", Dir: "/aibox/tasks/1/share", ReadOnly: true}}

	// act
	words := m.GuestWords()

	// assert
	assert.Equal(t, []string{"aibox.task"}, words, "the guest knows where to mount the task share")
}

func TestGuestWordsTellTheGuestHowASessionOnACloneStarts(t *testing.T) {
	for _, mode := range []string{vm.CloneNew, vm.CloneKeep} {
		// arrange
		m := machine()
		m.Clone = mode

		// act
		words := m.GuestWords()

		// assert
		assert.Equal(t, []string{"aibox.clone=" + mode}, words)
	}
}

func TestVirtiofsdArgsServeAReadOnlyShareReadOnly(t *testing.T) {
	for _, share := range []vm.Share{
		{Tag: "task", Dir: "/aibox/tasks/1/share", Socket: "/run/task.sock", ReadOnly: true},
		{Tag: "mount0", Dir: "/sdk/go", Socket: "/run/mount0.sock", Guest: "/opt/go"},
	} {
		// act
		args := share.VirtiofsdArgs(nil)

		// assert
		assert.True(t, share.IsReadOnly())
		assert.Contains(t, args, "--readonly")
	}
}
