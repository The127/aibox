// Package sandbox builds the bubblewrap command line that runs QEMU with
// nothing of the host but its own program, its libraries and firmware, the
// kernel, and the files it inherits.
package sandbox

import (
	"strconv"
	"strings"
)

// Kernel is where QEMU finds the kernel inside the sandbox, a copy that
// bubblewrap makes from the inherited file.
const Kernel = "/kernel"

// Spec is what QEMU needs inside the sandbox: the programs and files of
// the host that are bound in, and the number of the inherited file
// bubblewrap copies the kernel from.
type Spec struct {
	Bubblewrap string
	Program    string
	Libraries  string
	Firmware   string
	KernelFD   int
}

// Command returns bubblewrap and its arguments for running the program
// with the arguments inside the sandbox. The program and its libraries are
// bound read-only at their own paths, so that it starts as on the host.
func (s Spec) Command(args []string) (string, []string) {
	wrapped := []string{
		// every namespace, no terminal, no environment, and QEMU dies with aibox
		"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--ro-bind", s.Libraries, s.Libraries,
		// the loader of the program is named by its /lib64 path
		"--symlink", strings.TrimPrefix(s.Libraries, "/"), "/lib64",
		"--ro-bind", s.Program, s.Program,
		"--ro-bind", s.Firmware, s.Firmware,
		"--ro-bind-data", strconv.Itoa(s.KernelFD), Kernel,
		// QEMU probes file locking on /dev/null at start
		"--dev-bind", "/dev/null", "/dev/null",
		"--",
		s.Program,
	}

	return s.Bubblewrap, append(wrapped, args...)
}
