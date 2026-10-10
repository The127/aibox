package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffset is where the lock goes: a byte far beyond any data of the
// file. The lock of Windows keeps other handles from reading and writing
// the bytes it covers, so locking the data would keep a VMM from the disk.
// A lock on a byte no one reads or writes conflicts with other locks only,
// as flock does.
const lockOffset = 1 << 62

func lock(file *os.File, wait bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}

	overlapped := windows.Overlapped{Offset: uint32(lockOffset & 0xFFFFFFFF), OffsetHigh: uint32(lockOffset >> 32)}

	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}

	return err
}
