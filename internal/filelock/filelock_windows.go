package filelock

import (
	"errors"
	"math"
	"os"

	"golang.org/x/sys/windows"
)

// lock locks the whole file. Unlike flock, the lock of Windows keeps other
// handles from reading and writing the file, not only from locking it.
func lock(file *os.File, wait bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}

	var overlapped windows.Overlapped

	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, math.MaxUint32, math.MaxUint32, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}

	return err
}
