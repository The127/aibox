package fence

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

var errNoLandlock = errors.New("the kernel has no Landlock")

const (
	devNull = "/dev/null"
	// the Landlock ABI versions that added rights this package handles
	abiRefer    = 2
	abiTruncate = 3
)

// apply lets the calling thread, and what it starts, write only below dir
// and to /dev/null. Reading stays open.
func apply(dir string) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no new privileges: %w", err)
	}

	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || abi < 1 {
		return errNoLandlock
	}

	writes := writeAccess(abi)

	attr := unix.LandlockRulesetAttr{Handled_access_fs: writes}

	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0) //nolint:gosec // the kernel reads the struct
	if errno != 0 {
		return fmt.Errorf("create the Landlock ruleset: %w", errno)
	}

	defer func() { _ = unix.Close(int(ruleset)) }()

	// a file takes only the rights of files
	fileWrites := writes & (unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	if err := allow(int(ruleset), devNull, fileWrites); err != nil {
		return err
	}

	if dir != "" {
		if err := allow(int(ruleset), dir, writes); err != nil {
			return err
		}
	}

	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0); errno != 0 {
		return fmt.Errorf("restrict the helper with Landlock: %w", errno)
	}

	return nil
}

// writeAccess are the rights to change files of the ABI, so that each is
// denied unless a rule allows it.
func writeAccess(abi uintptr) uint64 {
	access := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
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

	return access
}

func allow(ruleset int, path string, access uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s for Landlock: %w", path, err)
	}

	defer func() { _ = unix.Close(fd) }()

	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(fd)} //nolint:gosec // a descriptor fits

	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 { //nolint:gosec // the kernel reads the struct
		return fmt.Errorf("allow writing below %s: %w", path, errno)
	}

	return nil
}
