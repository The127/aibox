package ninefs

import (
	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
)

// Lock takes, changes or releases a POSIX record lock of a process of the
// guest. A lock in the way answers Blocked, after which the guest's kernel
// waits and asks again for a blocking lock, and fails a non-blocking one.
func (f *file) Lock(pid int, lockType p9.LockType, _ p9.LockFlags, start, length uint64, client string) (p9.LockStatus, error) {
	if f.open == nil {
		return p9.LockStatusError, linux.EBADF
	}

	_, info, err := f.info()
	if err != nil {
		return p9.LockStatusError, err
	}

	key := fileKey{volume: info.VolumeSerialNumber, id: uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}
	owner := lockOwner{client: client, pid: int32(pid)} //nolint:gosec // the guest sends a 32-bit process id

	var read, write bool

	switch lockType {
	case p9.ReadLock:
		read = true
	case p9.WriteLock:
		write = true
	case p9.Unlock:
	default:
		return p9.LockStatusError, linux.EINVAL
	}

	if !f.fs.locks.set(key, owner, read, write, start, length) {
		return p9.LockStatusBlocked, nil
	}

	return p9.LockStatusOK, nil
}
