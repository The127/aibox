// Package filelock locks an open file for one process at a time, with the
// lock of the system: flock on Unix, LockFileEx on Windows. The lock stays
// with the file until it is closed, and with every copy of its descriptor.
package filelock

import (
	"errors"
	"os"
)

// ErrLocked is a file another process holds the lock on.
var ErrLocked = errors.New("the file is locked by another process")

// Lock takes the exclusive lock on the file. Unless it waits, it returns
// ErrLocked while another process holds the lock.
func Lock(file *os.File, wait bool) error {
	return lock(file, wait)
}
