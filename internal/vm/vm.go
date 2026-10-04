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

// reboot=t makes the kernel reset the machine with a triple fault, which
// -no-reboot below turns into a QEMU exit.
const baseCmdline = "root=/dev/vda rootfstype=ext4 rw console=hvc0 quiet panic=-1 reboot=t"

// Machine is a VM that boots a kernel with a root disk. Shell boots it into
// a shell instead of Claude Code. ProxyPort and TerminalPort are the vsock
// ports of the proxy and the terminal session on the host, and 0 leaves
// the port off the kernel command line. Owner is the host user the VM user
// stands for in the shares, and nil leaves the ids as they are.
type Machine struct {
	Kernel       string
	Rootfs       string
	MemoryMiB    int
	CPUs         int
	Shares       []Share
	GuestCID     uint32
	Shell        bool
	ProxyPort    uint32
	TerminalPort uint32
	Owner        *Owner
}

// Files are the numbers of the files QEMU was started with: the KVM and
// vhost-vsock devices, the kernel, the root disk, the socket QEMU writes
// the console to, and one connection to virtiofsd per entry of
// Machine.Shares, in the same order.
type Files struct {
	KVM     int
	Vhost   int
	Kernel  int
	Rootfs  int
	Console int
	Shares  []int
}

// Owner is a user on the host. In the shares, the VM user sees this user's
// files as its own. Files of other host users appear as nobody.
type Owner struct {
	UID uint32
	GID uint32
}

// Share is a host folder that virtiofsd serves to the VM. The guest mounts it
// by its tag. Guest is set for a share beyond the project and the home: the
// path the guest mounts it on, read-only.
type Share struct {
	Tag    string
	Dir    string
	Socket string
	Guest  string
}

// the fdsets of the files that QEMU opens by path again
const (
	kvmSet    = 1
	rootfsSet = 2
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
		"-chardev", "socket,id=console,fd=" + strconv.Itoa(files.Console),
		"-device", "virtio-serial-device",
		"-device", "virtconsole,chardev=console",
		// the kernel loader takes no fdset
		"-kernel", "/dev/fd/" + strconv.Itoa(files.Kernel),
		"-append", m.cmdline(),
		"-add-fd", fdset(files.Rootfs, rootfsSet),
		"-drive", "id=root,file=" + fdsetPath(rootfsSet) + ",format=raw,if=none,snapshot=on",
		"-device", "virtio-blk-device,drive=root",
	}

	for i, share := range m.Shares {
		id := "share-" + share.Tag
		args = append(args,
			"-chardev", "socket,id="+id+",fd="+strconv.Itoa(files.Shares[i]),
			"-device", "vhost-user-fs-device,chardev="+id+",tag="+share.Tag,
		)
	}

	return append(args, "-device", "vhost-vsock-device,guest-cid="+strconv.FormatUint(uint64(m.GuestCID), 10)+",vhostfd="+strconv.Itoa(files.Vhost))
}

func fdset(fd, set int) string {
	return "fd=" + strconv.Itoa(fd) + ",set=" + strconv.Itoa(set)
}

func fdsetPath(set int) string {
	return "/dev/fdset/" + strconv.Itoa(set)
}

func (m Machine) cmdline() string {
	words := []string{baseCmdline}
	if m.Shell {
		words = append(words, "aibox.shell")
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

	return strings.Join(words, " ")
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
	if s.Guest != "" {
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
