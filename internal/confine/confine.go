//go:build linux && amd64

// Package confine takes from the aibox process what it no longer needs
// once the VM runs: new privileges, the file system but for the resolver
// files, TCP but for connecting to the allowed ports, and the syscalls that
// reach into the kernel, other processes or namespaces.
package confine

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// errNoLandlock is a kernel without Landlock, or one too old to restrict
// TCP with it.
var errNoLandlock = errors.New("the kernel has no Landlock with network rules, needs Linux 6.7")

const (
	resolvConf = "/etc/resolv.conf"
	etc        = "/etc"
	dnsPort    = 53
	// the Landlock ABI versions that added rights this package handles
	abiRefer    = 2
	abiTruncate = 3
	abiNetwork  = 4
	abiIoctlDev = 5
	abiScopes   = 6
	// the rule type for a TCP port, which x/sys/unix does not name yet
	ruleNetPort = 2
	// the x32 syscalls carry this bit on an x86_64 kernel
	x32Bit = 0x40000000
)

// Apply confines the process and all its threads. Ports are the TCP ports
// it may still connect to. It must run after every child of the process
// has started, because children inherit it. A program built with cgo
// cannot reach its other threads and gets an error; aibox is built without
// cgo.
func Apply(ports []uint16) error {
	return apply(ports, allThreads)
}

// ApplyToThread confines the calling thread alone, for a test binary built
// with cgo. Everything the thread starts inherits it.
func ApplyToThread(ports []uint16) error {
	return apply(ports, thisThread)
}

type restrict func(trap, a1, a2, a3 uintptr) error

func apply(ports []uint16, on restrict) error {
	// Landlock and seccomp need this, and a setuid program could not give
	// the process anything any more
	if err := on(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0); err != nil {
		return fmt.Errorf("set no new privileges: %w", err)
	}

	if err := landlock(ports, on); err != nil {
		return err
	}

	if err := seccomp(); err != nil {
		return fmt.Errorf("install the syscall filter: %w", err)
	}

	return nil
}

func allThreads(trap, a1, a2, a3 uintptr) error {
	_, _, errno := syscall.AllThreadsSyscall(trap, a1, a2, a3)
	if errno == unix.ENOTSUP {
		return errors.New("the program is built with cgo and cannot confine its threads")
	}

	if errno != 0 {
		return errno
	}

	return nil
}

func thisThread(trap, a1, a2, a3 uintptr) error {
	if _, _, errno := unix.Syscall(trap, a1, a2, a3); errno != 0 {
		return errno
	}

	return nil
}

// landlock leaves reading the resolver files and connecting to the ports,
// and to the DNS port, because the resolver falls back to TCP for large
// answers.
func landlock(ports []uint16, on restrict) error {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || abi < abiNetwork {
		return errNoLandlock
	}

	// aibox has to signal its children, which are outside the domain, so
	// the signal scope stays off
	attr := unix.LandlockRulesetAttr{
		Access_fs:  fileAccess(abi),
		Access_net: unix.LANDLOCK_ACCESS_NET_CONNECT_TCP | unix.LANDLOCK_ACCESS_NET_BIND_TCP,
	}

	if abi >= abiScopes {
		attr.Scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET
	}

	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0) //nolint:gosec // the kernel reads the struct
	if errno != 0 {
		return fmt.Errorf("create the Landlock ruleset: %w", errno)
	}

	defer func() { _ = unix.Close(int(ruleset)) }()

	for _, dir := range readableDirs() {
		if err := allowReading(int(ruleset), dir); err != nil {
			return err
		}
	}

	for _, port := range append([]uint16{dnsPort}, ports...) {
		if err := allowConnecting(int(ruleset), port); err != nil {
			return err
		}
	}

	if err := on(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0); err != nil {
		return fmt.Errorf("restrict the process with Landlock: %w", err)
	}

	return nil
}

// fileAccess are the file system rights of the ABIs up to 6, so that each
// is denied unless a rule allows it. Rights that newer kernels added are
// not handled and stay unrestricted.
func fileAccess(abi uintptr) uint64 {
	access := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)

	if abi >= abiRefer {
		access |= unix.LANDLOCK_ACCESS_FS_REFER
	}

	if abi >= abiTruncate {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}

	if abi >= abiIoctlDev {
		access |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}

	return access
}

// readableDirs are /etc and the folder resolv.conf really lives in, which
// on a host with systemd-resolved is under /run.
func readableDirs() []string {
	dirs := []string{etc}

	if target, err := filepath.EvalSymlinks(resolvConf); err == nil {
		if dir := filepath.Dir(target); dir != etc {
			dirs = append(dirs, dir)
		}
	}

	return dirs
}

func allowReading(ruleset int, dir string) error {
	fd, err := unix.Open(dir, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s for Landlock: %w", dir, err)
	}

	defer func() { _ = unix.Close(fd) }()

	rule := unix.LandlockPathBeneathAttr{
		Allowed_access: unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR,
		Parent_fd:      int32(fd), //nolint:gosec // a descriptor fits
	}

	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 { //nolint:gosec // the kernel reads the struct
		return fmt.Errorf("allow reading %s: %w", dir, errno)
	}

	return nil
}

// netPortAttr is the Landlock rule for a TCP port, as the kernel lays it
// out.
type netPortAttr struct {
	allowedAccess uint64
	port          uint64
}

func allowConnecting(ruleset int, port uint16) error {
	rule := netPortAttr{allowedAccess: unix.LANDLOCK_ACCESS_NET_CONNECT_TCP, port: uint64(port)}

	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), ruleNetPort, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 { //nolint:gosec // the kernel reads the struct
		return fmt.Errorf("allow connecting to port %d: %w", port, errno)
	}

	return nil
}

// denied are the syscalls aibox has no use for. The first group reaches
// into other processes or the kernel, the second into mounts and
// namespaces, the third changes the host, and the last two give a process
// a different view of itself.
var denied = []uint32{
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN,
	unix.SYS_USERFAULTFD, unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,

	unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT, unix.SYS_OPEN_TREE, unix.SYS_MOVE_MOUNT,
	unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_FSPICK, unix.SYS_MOUNT_SETATTR,
	unix.SYS_UNSHARE, unix.SYS_SETNS,

	unix.SYS_KEXEC_LOAD, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
	unix.SYS_REBOOT, unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_ACCT, unix.SYS_QUOTACTL,
	unix.SYS_SETTIMEOFDAY, unix.SYS_CLOCK_SETTIME, unix.SYS_ADJTIMEX, unix.SYS_CLOCK_ADJTIME,

	unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_PERSONALITY,
}

// seccomp installs a filter on every thread that refuses the denied
// syscalls with EPERM and lets everything else through. Another
// architecture, and the x32 numbers of this one, are refused as a whole.
func seccomp() error {
	// the offsets of the syscall number and the architecture in the data
	// the kernel hands the filter
	const (
		nrOffset   = 0
		archOffset = 4
	)

	refuse := unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)}

	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: archOffset},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AUDIT_ARCH_X86_64, Jt: 1, Jf: 0},
		refuse,
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: nrOffset},
		{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: x32Bit, Jt: 0, Jf: 1},
		refuse,
	}

	for _, nr := range denied {
		filter = append(filter, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: nr, Jt: 0, Jf: 1}, refuse)
	}

	filter = append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})

	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]} //nolint:gosec // the filter is short

	// with TSYNC a thread that could not be filtered comes back as the
	// result, without an errno
	failed, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog))) //nolint:gosec // the kernel reads the struct
	if errno != 0 {
		return errno
	}

	if failed != 0 {
		return fmt.Errorf("thread %d could not be filtered", failed)
	}

	return nil
}
