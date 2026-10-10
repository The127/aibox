package guest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

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

const scsiDiskClass = "/sys/class/scsi_disk"

const (
	// scsiDiskWait is how long SCSIDisk waits for a disk to be listed. The
	// kernel ends probing the disk of the root before it mounts the root,
	// and the backend puts both disks on one controller, so the wait is
	// only for a controller that is slower than that.
	scsiDiskWait = 5 * time.Second
	scsiDiskPoll = 50 * time.Millisecond
)

// SCSIDisk is the device of the SCSI disk at the LUN of the first target,
// found by its address, since the name depends on the order of probing.
func (Linux) SCSIDisk(lun int) (string, error) {
	return scsiDisk(scsiDiskClass, lun, scsiDiskWait)
}

// scsiDisk refuses a second disk at the LUN at once, so that another
// controller cannot hand over a disk of its own.
func scsiDisk(class string, lun int, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)

	for {
		found, err := scsiDisksAt(class, lun)
		if err != nil {
			return "", err
		}

		if len(found) == 1 {
			return found[0], nil
		}

		if len(found) > 1 || time.Now().After(deadline) {
			return "", fmt.Errorf("%w at LUN %d: found %d", ErrNoSCSIDisk, lun, len(found))
		}

		time.Sleep(scsiDiskPoll)
	}
}

// The entries of class are named host:channel:target:lun.
func scsiDisksAt(class string, lun int) ([]string, error) {
	entries, err := os.ReadDir(class)
	if err != nil {
		return nil, err
	}

	var found []string

	for _, entry := range entries {
		address := strings.Split(entry.Name(), ":")
		if len(address) != 4 || address[2] != "0" || address[3] != strconv.Itoa(lun) {
			continue
		}

		// the kernel lists the address a moment before the block device
		blocks, err := os.ReadDir(filepath.Join(class, entry.Name(), "device", "block"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return nil, err
		}

		for _, block := range blocks {
			found = append(found, "/dev/"+block.Name())
		}
	}

	return found, nil
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

const (
	// Hyper-V serves every Plan 9 share of a VM on this one port, told apart
	// by their names.
	plan9Port = 564
	// plan9MessageSize is the largest Plan 9 message the kernel offers the
	// server of Hyper-V, which may agree on a smaller one.
	plan9MessageSize = 65536
)

// MountPlan9 connects to the Plan 9 server of Hyper-V over vsock and mounts
// the share of the name on the target through that connection. The kernel
// keeps the connection, so its descriptor here is closed once mounted. It is
// a plain socket rather than one of DialHost, since the kernel needs only
// its number and Go's poller would have nothing to do with it.
func (l Linux) MountPlan9(share, target string, flags uintptr) error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open a vsock socket: %w", err)
	}

	defer func() { _ = unix.Close(fd) }()

	if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: plan9Port}); err != nil {
		return fmt.Errorf("connect to the Plan 9 server of the host: %w", err)
	}

	return l.Mount(share, target, "9p", flags, plan9Options(fd, share))
}

// plan9Options hands the connection to the kernel as both ends of the
// transport, and names the share, since one server serves them all.
func plan9Options(fd int, share string) string {
	return fmt.Sprintf("trans=fd,rfdno=%d,wfdno=%d,msize=%d,aname=%s", fd, fd, plan9MessageSize, share)
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

// killTimeout is how long Kill waits for the processes of the cgroup to
// end.
const killTimeout = 10 * time.Second

var errStillPopulated = errors.New("processes are left after the kill")

// Kill ends every process in the cgroup and below with cgroup.kill, and
// waits until cgroup.events says the cgroup is empty.
func (Linux) Kill(cgroup string) error {
	if err := os.WriteFile(cgroup+"/cgroup.kill", []byte("1"), 0); err != nil {
		return err
	}

	deadline := time.Now().Add(killTimeout)

	for {
		events, err := os.ReadFile(cgroup + "/cgroup.events") //nolint:gosec // the cgroup is fixed
		if err != nil {
			return err
		}

		if !slices.Contains(strings.Split(string(events), "\n"), "populated 1") {
			return nil
		}

		if time.Now().After(deadline) {
			return errStillPopulated
		}

		time.Sleep(10 * time.Millisecond)
	}
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

// Stderr is the standard error of the init.
func (Linux) Stderr() io.Writer { return os.Stderr }

// Halt writes the file systems out and ends the machine, the way that ends
// the VM on this architecture.
func (Linux) Halt() error {
	syscall.Sync()

	return syscall.Reboot(haltCommand)
}
