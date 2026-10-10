//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// noFollowFlags open a file without following a link, without waiting for
// a FIFO and without making a terminal the controlling one.
const noFollowFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_NOCTTY

// openNoFollow opens the file at the path with the flags and noFollowFlags.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|noFollowFlags, perm) //nolint:gosec // the callers say where the path comes from
}

// refuseLink does nothing: O_NOFOLLOW in noFollowFlags refuses a link at
// the end of the path when the file is opened.
func refuseLink(*os.Root, string) error { return nil }
