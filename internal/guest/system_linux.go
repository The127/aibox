package guest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"github.com/the127/aibox/internal/vm"
)

// Linux is the System of the running kernel.
type Linux struct{}

// Mount creates the target folder and mounts a file system on it.
func (Linux) Mount(source, target, fstype string, flags uintptr, data string) error {
	if err := os.MkdirAll(target, 0o755); err != nil { //nolint:gosec // a mount point everyone may enter
		return err
	}

	return syscall.Mount(source, target, fstype, flags, data)
}

const (
	// the superblock of ext4 is the KiB after the first and keeps its
	// magic 56 bytes in
	superblockOffset = 1024
	superblockSize   = 1024
	ext4MagicOffset  = 56
	ext4Magic        = 0xEF53
	mke2fs           = "/usr/sbin/mke2fs"
)

// Mkdir makes the folder and its parents, if missing.
func (Linux) Mkdir(path string) error {
	return os.MkdirAll(path, 0o755) //nolint:gosec // a folder everyone may enter
}

// Chmod sets the mode of the file.
func (Linux) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

// Pin binds the file or folder on itself, which no one without
// CAP_SYS_ADMIN can undo. A symlink is refused, because the mount would
// sit on its target while the link itself stays replaceable.
func (Linux) Pin(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}

	return syscall.Mount(path, path, "", syscall.MS_BIND, "")
}

// Protect pins the file or folder and makes the mount read-only.
func (l Linux) Protect(path string) error {
	if err := l.Pin(path); err != nil {
		return err
	}

	return syscall.Mount("", path, "", syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_RDONLY, "")
}

// PivotRoot makes newRoot the root, moves the old root to putOld inside
// it, and detaches it from there.
func (Linux) PivotRoot(newRoot, putOld string) error {
	if err := os.MkdirAll(newRoot+putOld, 0o755); err != nil { //nolint:gosec // a mount point everyone may enter
		return err
	}

	if err := syscall.PivotRoot(newRoot, newRoot+putOld); err != nil {
		return err
	}

	if err := os.Chdir("/"); err != nil {
		return err
	}

	return syscall.Unmount(putOld, syscall.MNT_DETACH)
}

// Blank tells whether the disk is empty, judged by its superblock.
func (Linux) Blank(device string) (bool, error) {
	disk, err := os.Open(device) //nolint:gosec // the device is fixed
	if err != nil {
		return false, err
	}

	defer func() { _ = disk.Close() }()

	superblock := make([]byte, superblockSize)
	if _, err := disk.ReadAt(superblock, superblockOffset); err != nil {
		return false, err
	}

	if binary.LittleEndian.Uint16(superblock[ext4MagicOffset:]) == ext4Magic {
		return false, nil
	}

	if len(bytes.Trim(superblock, "\x00")) != 0 {
		return false, ErrDamaged
	}

	return true, nil
}

// Format puts an ext4 file system on the disk.
func (Linux) Format(device string) error {
	cmd := exec.Command(mke2fs, "-t", "ext4", "-q", "-F", device) //nolint:gosec // the device is fixed

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// Own makes the folder, if missing, and gives it to the user.
func (Linux) Own(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil { //nolint:gosec // a folder everyone may enter
		return err
	}

	return os.Chown(path, int(vm.GuestUID), int(vm.GuestGID))
}

// Symlink creates a symbolic link.
func (Linux) Symlink(target, path string) error {
	return os.Symlink(target, path)
}

// ReadCmdline returns the kernel command line.
func (Linux) ReadCmdline() (string, error) {
	content, err := os.ReadFile("/proc/cmdline")

	return strings.TrimSpace(string(content)), err
}

// OpenConsole opens the terminal for reading and writing.
func (Linux) OpenConsole(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // the path comes from the kernel command line
}

// BringLoopbackUp brings the loopback interface up. The kernel then gives it
// 127.0.0.1 by itself.
func (Linux) BringLoopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}

	defer func() { _ = unix.Close(fd) }()

	request, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}

	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, request); err != nil {
		return err
	}

	request.SetUint16(request.Uint16() | unix.IFF_UP)

	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request)
}

// Listen listens on a TCP address.
func (Linux) Listen(address string) (net.Listener, error) {
	return net.Listen("tcp", address)
}

// DialHost connects to the host over vsock.
func (Linux) DialHost(port uint32) (net.Conn, error) {
	return vsock.Dial(vsock.Host, port, nil)
}

// Sethostname sets the hostname of the VM.
func (Linux) Sethostname(name string) error {
	return syscall.Sethostname([]byte(name))
}

// Start starts the command and returns its PID.
func (Linux) Start(cmd *exec.Cmd, cgroup string) (int, error) {
	dir, err := os.Open(cgroup) //nolint:gosec // the cgroup is fixed
	if err != nil {
		return 0, err
	}

	defer func() { _ = dir.Close() }()

	// the kernel puts the child into the cgroup before it runs anything
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(dir.Fd())

	if err := cmd.Start(); err != nil {
		return 0, err
	}

	return cmd.Process.Pid, nil
}

// cgroupControllers are all the controllers the kernel of the image has.
const cgroupControllers = "+cpuset +cpu +io +memory +pids"

// Controllers turns the controllers on for the cgroups below.
func (Linux) Controllers(cgroup string) error {
	return os.WriteFile(cgroup+"/cgroup.subtree_control", []byte(cgroupControllers), 0)
}

// Delegate makes the cgroup and gives it to the user, with the files the
// kernel documents for delegation.
func (Linux) Delegate(cgroup string) error {
	if err := os.Mkdir(cgroup, 0o755); err != nil && !errors.Is(err, fs.ErrExist) { //nolint:gosec // a cgroup everyone may read
		return err
	}

	for _, name := range []string{"", "/cgroup.procs", "/cgroup.subtree_control", "/cgroup.threads"} {
		if err := os.Chown(cgroup+name, int(vm.GuestUID), int(vm.GuestGID)); err != nil {
			return err
		}
	}

	return nil
}

// Wait waits for any child to exit and returns its PID and exit code. A
// child ended by a signal gets 128 plus the signal number, like in a shell.
func (Linux) Wait() (int, int, error) {
	var status syscall.WaitStatus

	pid, err := syscall.Wait4(-1, &status, 0, nil)
	for errors.Is(err, syscall.EINTR) {
		pid, err = syscall.Wait4(-1, &status, 0, nil)
	}

	if err != nil {
		return 0, 0, err
	}

	if status.Signaled() {
		return pid, 128 + int(status.Signal()), nil
	}

	return pid, status.ExitStatus(), nil
}

// Halt writes the file systems out and resets the machine, which ends the
// VM because QEMU runs with -no-reboot.
func (Linux) Halt() error {
	syscall.Sync()

	// the kernel has no ACPI, so it cannot power off
	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
}
