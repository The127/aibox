// Package vm builds the command lines of QEMU and virtiofsd for the aibox VM.
package vm

import (
	"fmt"
	"strconv"
	"strings"
)

// GuestUID and GuestGID are the user Claude Code runs as in the VM.
const (
	GuestUID uint32 = 1000
	GuestGID uint32 = 1000
)

// GuestCID is the vsock address of the VM. Every VM has the same one,
// because each has a vsock namespace of its own.
const GuestCID uint32 = 3

// RootCmdline boots the root disk read-only on the virtio console, whichever
// VMM boots the VM. The root stays read-only, as the VMM opened it: the init
// mounts an overlay for what it has to write.
const RootCmdline = "root=/dev/vda rootfstype=ext4 ro console=hvc0 quiet"

// reboot=t makes the kernel reset the machine with a triple fault, which
// -no-reboot below turns into a QEMU exit.
const baseCmdline = RootCmdline + " panic=-1 reboot=t"

// Machine is a VM that boots a kernel with a root disk and keeps its state
// on a second disk. Shell boots it into a shell instead of Claude Code, Task
// runs the task of the task share unattended. HyperV tells the guest it
// runs on Hyper-V, which shares the folders of the host over Plan 9 and has
// SCSI disks, instead of virtio-fs and virtio disks.
// ProxyPort and TerminalPort are the vsock ports of the proxy and the
// terminal session on the host, and 0 leaves the port off the kernel
// command line. Loopback are the ports on the loopback of the host the VM
// reaches through the proxy. Owner is the host user the VM user stands for
// in the shares, and nil leaves the ids as they are.
type Machine struct {
	Kernel string
	// KernelDigest is the SHA-256 the kernel must have, or empty
	KernelDigest string
	Rootfs       string
	State        string
	MemoryMiB    int
	CPUs         int
	Shares       []Share
	Shell        bool
	Task         bool
	HyperV       bool
	ProxyPort    uint32
	TerminalPort uint32
	Loopback     []uint16
	Owner        *Owner
}

// Files are the numbers of the files QEMU was started with: the KVM and
// vhost-vsock devices, the root disk, the state disk opened read-only and
// once more read-write, the socket QEMU writes the console to, and one
// connection to virtiofsd per entry of Machine.Shares, in the same order.
// Kernel is a path, not a number, because the kernel loader of QEMU cannot
// take a descriptor. The state disk comes in both modes because QEMU opens
// a drive read-only first and asks the fdset for a read-write descriptor
// when the device attaches.
type Files struct {
	KVM        int
	Vhost      int
	Kernel     string
	Rootfs     int
	StateRead  int
	StateWrite int
	Console    int
	Shares     []int
}

// seccomp is the syscall filter QEMU puts on itself: no syscalls it does
// not need, no new privileges, no child processes, no resource control.
const seccomp = "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny"

// Owner is a user on the host. In the shares, the VM user sees this user's
// files as its own. Files of other host users appear as nobody.
type Owner struct {
	UID uint32
	GID uint32
}

// Share is a host folder that virtiofsd serves to the VM. The guest mounts it
// by its tag. Guest is set for a share beyond the project and the home: the
// path the guest mounts it on, read-only. ReadOnly makes a share read-only
// that the guest knows where to mount by its tag.
type Share struct {
	Tag      string
	Dir      string
	Socket   string
	Guest    string
	ReadOnly bool
}

// IsReadOnly tells whether the VM may only read the share.
func (s Share) IsReadOnly() bool {
	return s.ReadOnly || s.Guest != ""
}

// the fdsets of the files that QEMU opens by path again
const (
	kvmSet    = 1
	rootfsSet = 2
	stateSet  = 3
)

// QEMUArgs returns the arguments for qemu-system-x86_64. QEMU gets every
// file through the numbers in files, so that it opens no path of the host.
func (m Machine) QEMUArgs(files Files) []string {
	memory := strconv.Itoa(m.MemoryMiB) + "M"

	args := []string{
		// the kernel has no ACPI, see image/microvm.config. Without an RTC the
		// kernel spends seconds at boot waiting for the time. Option ROMs would
		// be files QEMU reads from the host.
		"-machine", "microvm,acpi=off,rtc=on,memory-backend=mem,x-option-roms=off",
		"-add-fd", fdset(files.KVM, kvmSet),
		"-accel", "kvm,device=" + fdsetPath(kvmSet),
		"-cpu", "host",
		"-smp", strconv.Itoa(m.CPUs),
		"-m", memory,
		// virtiofsd reads and writes the guest memory directly
		"-object", "memory-backend-memfd,id=mem,size=" + memory + ",share=on",
		// the VM ends itself with a reset, which -no-reboot turns into an exit
		"-nodefaults", "-no-user-config", "-display", "none", "-no-reboot",
		"-sandbox", seccomp,
		"-chardev", "socket,id=console,fd=" + strconv.Itoa(files.Console),
		"-device", "virtio-serial-device",
		"-device", "virtconsole,chardev=console",
		"-kernel", files.Kernel,
		"-append", m.cmdline(),
		"-add-fd", fdset(files.Rootfs, rootfsSet),
		"-drive", "id=root,file=" + fdsetPath(rootfsSet) + ",format=raw,if=none,read-only=on",
		"-device", "virtio-blk-device,drive=root",
		"-add-fd", fdset(files.StateRead, stateSet),
		"-add-fd", fdset(files.StateWrite, stateSet),
		"-drive", "id=state,file=" + fdsetPath(stateSet) + ",format=raw,if=none",
		"-device", "virtio-blk-device,drive=state",
	}

	for i, share := range m.Shares {
		id := "share-" + share.Tag
		args = append(args,
			"-chardev", "socket,id="+id+",fd="+strconv.Itoa(files.Shares[i]),
			"-device", "vhost-user-fs-device,chardev="+id+",tag="+share.Tag,
		)
	}

	return append(args, "-device", "vhost-vsock-device,guest-cid="+strconv.FormatUint(uint64(GuestCID), 10)+",vhostfd="+strconv.Itoa(files.Vhost))
}

func fdset(fd, set int) string {
	return "fd=" + strconv.Itoa(fd) + ",set=" + strconv.Itoa(set)
}

func fdsetPath(set int) string {
	return "/dev/fdset/" + strconv.Itoa(set)
}

func (m Machine) cmdline() string {
	return m.Cmdline(baseCmdline)
}

// Cmdline is the kernel command line that starts with what the VMM needs and
// goes on with the words of the guest.
func (m Machine) Cmdline(start string) string {
	return strings.Join(append([]string{start}, m.GuestWords()...), " ")
}

// GuestWords are the words of the kernel command line that tell the guest
// its settings, the vsock ports of the host, the mounts of the host and the
// ports on the loopback of the host it may reach, whichever VMM boots it.
func (m Machine) GuestWords() []string {
	var words []string
	if m.Shell {
		words = append(words, "aibox.shell")
	}

	if m.Task {
		words = append(words, "aibox.task")
	}

	if m.HyperV {
		words = append(words, "aibox.hyperv")
	}

	if m.ProxyPort != 0 {
		words = append(words, "aibox.proxy="+strconv.FormatUint(uint64(m.ProxyPort), 10))
	}

	if m.TerminalPort != 0 {
		words = append(words, "aibox.terminal="+strconv.FormatUint(uint64(m.TerminalPort), 10))
	}

	for _, share := range m.Shares {
		if share.Guest != "" {
			words = append(words, "aibox.mount="+share.Tag+":"+share.Guest)
		}
	}

	if len(m.Loopback) > 0 {
		ports := make([]string, len(m.Loopback))
		for i, port := range m.Loopback {
			ports[i] = strconv.FormatUint(uint64(port), 10)
		}

		words = append(words, "aibox.loopback="+strings.Join(ports, ","))
	}

	return words
}

// VirtiofsdArgs returns the arguments for virtiofsd that serve the share to
// the VM user standing for the owner.
func (s Share) VirtiofsdArgs(owner *Owner) []string {
	args := []string{
		"--socket-path=" + s.Socket,
		"--shared-dir=" + s.Dir,
	}

	// the VM cannot change the folder, and a change on the host may wait for
	// the next run, so names and attributes are cached for the whole run
	if s.IsReadOnly() {
		args = append(args, "--readonly", "--cache=always")
	}

	if owner == nil {
		return args
	}

	// virtiofsd takes one flag per direction
	return append(args,
		fmt.Sprintf("--translate-uid=guest:%d:%d:1", GuestUID, owner.UID),
		fmt.Sprintf("--translate-uid=host:%d:%d:1", owner.UID, GuestUID),
		fmt.Sprintf("--translate-gid=guest:%d:%d:1", GuestGID, owner.GID),
		fmt.Sprintf("--translate-gid=host:%d:%d:1", owner.GID, GuestGID),
	)
}
