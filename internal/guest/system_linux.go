package guest

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
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

// Poweroff writes the file systems out and turns the VM off.
func (Linux) Poweroff() error {
	syscall.Sync()

	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}
