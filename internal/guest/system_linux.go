package guest

import (
	"encoding/binary"
	"errors"
	"fmt"
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
	// the superblock of ext4 starts at 1024 and keeps its magic 56 bytes in
	ext4MagicOffset = 1024 + 56
	ext4Magic       = 0xEF53
	mke2fs          = "/usr/sbin/mke2fs"
)

// Mkdir makes the folder and its parents, if missing.
func (Linux) Mkdir(path string) error {
	return os.MkdirAll(path, 0o755) //nolint:gosec // a folder everyone may enter
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

// Blank tells whether the disk has no ext4 file system yet.
func (Linux) Blank(device string) (bool, error) {
	disk, err := os.Open(device) //nolint:gosec // the device is fixed
	if err != nil {
		return false, err
	}

	defer func() { _ = disk.Close() }()

	magic := make([]byte, 2)
	if _, err := disk.ReadAt(magic, ext4MagicOffset); err != nil {
		return false, err
	}

	return binary.LittleEndian.Uint16(magic) != ext4Magic, nil
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
func (Linux) Start(cmd *exec.Cmd) (int, error) {
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	return cmd.Process.Pid, nil
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
