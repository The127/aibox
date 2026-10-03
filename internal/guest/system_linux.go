package guest

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
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
