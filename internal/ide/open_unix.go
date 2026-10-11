//go:build !windows

package ide

import (
	"os"
	"syscall"
)

// openLock opens a lock file without following a symlink, and without
// waiting on a FIFO, either of which could have taken its place since the
// folder was read. Waiting would hang aibox before it can see what the
// file is.
func openLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // the path is a file in the folder of the lock files
}
