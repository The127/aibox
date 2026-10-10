//go:build !windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lock(file *os.File, wait bool) error {
	how := unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}

	err := unix.Flock(int(file.Fd()), how)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrLocked
	}

	return err
}
