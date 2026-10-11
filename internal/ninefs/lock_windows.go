package ninefs

import (
	"math"

	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
)

// Lock takes, changes or releases a POSIX record lock of a process of the
// guest. A lock in the way answers Blocked, after which the guest's kernel
// waits and asks again for a blocking lock, and fails a non-blocking one.
func (f *file) Lock(pid int, lockType p9.LockType, _ p9.LockFlags, start, length uint64, client string) (p9.LockStatus, error) {
	if f.open == 0 {
		return p9.LockStatusError, linux.EBADF
	}

	key, err := f.key()
	if err != nil {
		return p9.LockStatusError, err
	}

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

// GetLock tells the guest which lock is in the way of the one it asks
// about, as fcntl(F_GETLK) does, or the type Unlock when none is.
func (f *file) GetLock(pid int, lockType p9.LockType, start, length uint64, client string) (p9.LockType, uint64, uint64, int, string, error) {
	if f.open == 0 {
		return 0, 0, 0, 0, "", linux.EBADF
	}

	if lockType != p9.ReadLock && lockType != p9.WriteLock {
		return 0, 0, 0, 0, "", linux.EINVAL
	}

	key, err := f.key()
	if err != nil {
		return 0, 0, 0, 0, "", err
	}

	l, ok := f.fs.locks.conflict(key, lockOwner{client: client, pid: int32(pid)}, lockType == p9.WriteLock, start, length) //nolint:gosec // the guest sends a 32-bit process id
	if !ok {
		return p9.Unlock, start, length, pid, client, nil
	}

	typ := p9.ReadLock
	if l.write {
		typ = p9.WriteLock
	}

	// a lock to the end of the file has the length 0 again
	held := l.end - l.start
	if l.end == math.MaxUint64 {
		held = 0
	}

	return typ, l.start, held, int(l.owner.pid), l.owner.client, nil
}

// key is the file behind the handle, whichever name the guest reached it
// by.
func (f *file) key() (fileKey, error) {
	_, info, err := f.info()
	if err != nil {
		return fileKey{}, err
	}

	return fileKey{volume: info.VolumeSerialNumber, id: info.id()}, nil
}
