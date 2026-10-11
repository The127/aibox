//go:build linux || (darwin && cgo)

package fence

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

const confines = true

var errJob = errors.New("the helper takes exec <folder> <program> [<argument>...] or write <folder> <name>")

// Serve is the helper. It confines itself to the folder of the job, then
// runs the program of an exec job in its place, or writes what comes in
// to the file of a write job.
func Serve(args []string, in io.Reader) error {
	if len(args) < 3 {
		return errJob
	}

	job, dir, rest := args[0], args[1], args[2:]
	if job != execJob && (job != writeJob || len(rest) != 1) {
		return errJob
	}

	// Landlock confines the calling thread, which then does the write or
	// becomes the program
	runtime.LockOSThread()

	if err := apply(dir); err != nil {
		return err
	}

	if job == writeJob {
		return writeFile(dir, rest[0], in)
	}

	program, err := exec.LookPath(rest[0])
	if err != nil {
		return err
	}

	return syscall.Exec(program, rest, os.Environ()) //nolint:gosec // the caller's program
}
