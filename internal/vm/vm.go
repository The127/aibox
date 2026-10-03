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

const baseCmdline = "root=/dev/vda rootfstype=ext4 rw console=ttyS0 quiet panic=-1"

// Machine is a VM that boots a kernel with a root disk. Shell boots it into
// a shell instead of Claude Code. ProxyPort is the vsock port of the proxy
// on the host, and 0 means there is none. Owner is the host user the VM
// user stands for in the shares, and nil leaves the ids as they are.
type Machine struct {
	Kernel    string
	Rootfs    string
	MemoryMiB int
	CPUs      int
	Shares    []Share
	GuestCID  uint32
	Shell     bool
	ProxyPort uint32
	Owner     *Owner
}

// Owner is a user on the host. In the shares, the VM user sees this user's
// files as its own. Files of other host users appear as nobody.
type Owner struct {
	UID uint32
	GID uint32
}

// Share is a host folder that virtiofsd serves to the VM. The guest mounts it
// by its tag.
type Share struct {
	Tag    string
	Dir    string
	Socket string
}

// QEMUArgs returns the arguments for qemu-system-x86_64.
func (m Machine) QEMUArgs() []string {
	memory := strconv.Itoa(m.MemoryMiB) + "M"

	args := []string{
		// without an RTC the kernel spends seconds at boot waiting for the time
		"-machine", "microvm,acpi=on,rtc=on,memory-backend=mem",
		"-enable-kvm", "-cpu", "host",
		"-smp", strconv.Itoa(m.CPUs),
		"-m", memory,
		// virtiofsd reads and writes the guest memory directly
		"-object", "memory-backend-memfd,id=mem,size=" + memory + ",share=on",
		"-nodefaults", "-no-user-config", "-nographic", "-no-reboot",
		"-serial", "mon:stdio",
		"-kernel", m.Kernel,
		"-append", m.cmdline(),
		"-drive", "id=root,file=" + escape(m.Rootfs) + ",format=raw,if=none,snapshot=on",
		"-device", "virtio-blk-device,drive=root",
	}

	for _, share := range m.Shares {
		id := "share-" + escape(share.Tag)
		args = append(args,
			"-chardev", "socket,id="+id+",path="+escape(share.Socket),
			"-device", "vhost-user-fs-device,chardev="+id+",tag="+escape(share.Tag),
		)
	}

	return append(args, "-device", "vhost-vsock-device,guest-cid="+strconv.FormatUint(uint64(m.GuestCID), 10))
}

func (m Machine) cmdline() string {
	words := []string{baseCmdline}
	if m.Shell {
		words = append(words, "aibox.shell")
	}

	if m.ProxyPort != 0 {
		words = append(words, "aibox.proxy="+strconv.FormatUint(uint64(m.ProxyPort), 10))
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

// escape doubles commas, because QEMU separates the parts of an option value
// with commas.
func escape(value string) string {
	return strings.ReplaceAll(value, ",", ",,")
}
