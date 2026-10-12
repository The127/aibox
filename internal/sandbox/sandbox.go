// Package sandbox builds the bubblewrap command line that runs QEMU with
// nothing of the host but its own program, its libraries and firmware, the
// kernel, and the files it inherits.
package sandbox

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

// Kernel is where QEMU finds the kernel inside the sandbox, a copy that
// bubblewrap makes from the inherited file.
const Kernel = "/kernel"

// Spec is what QEMU needs inside the sandbox: the programs and files of
// the host that are bound in, and the number of the inherited file
// bubblewrap copies the kernel from. Libraries are the loader and the
// libraries of the program.
type Spec struct {
	Bubblewrap string
	Program    string
	Libraries  []string
	Firmware   string
	KernelFD   int
}

// Command returns bubblewrap and its arguments for running the program
// with the arguments inside the sandbox. The program and its libraries are
// bound read-only at their own paths, so that it starts as on the host.
func (s Spec) Command(args []string) (string, []string) {
	wrapped := []string{
		// every namespace, no terminal, no environment but LD_LIBRARY_PATH
		// below, and QEMU dies with aibox
		"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
	}

	var folders []string

	for _, library := range s.Libraries {
		wrapped = append(wrapped, "--ro-bind", library, library)

		if folder := path.Dir(library); !slices.Contains(folders, folder) {
			folders = append(folders, folder)
		}
	}

	// the loader in the sandbox has no ld.so.cache from the host, which can name
	// folders it would not search on its own
	wrapped = append(wrapped, "--setenv", "LD_LIBRARY_PATH", strings.Join(folders, ":"))

	wrapped = append(wrapped,
		"--ro-bind", s.Program, s.Program,
		"--ro-bind", s.Firmware, s.Firmware,
		"--ro-bind-data", strconv.Itoa(s.KernelFD), Kernel,
		// QEMU probes file locking on /dev/null at start
		"--dev-bind", "/dev/null", "/dev/null",
		"--",
		s.Program,
	)

	return s.Bubblewrap, append(wrapped, args...)
}
