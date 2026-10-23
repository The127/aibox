//go:build linux

package launch

import (
	"os/exec"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/vm"
)

// Prepare readies the host for the spec and returns what Run of the Backend
// hands to Run of the package.
func (b Backend) Prepare(spec backend.Spec) (vm.Machine, Options, error) {
	return b.prepare(spec)
}

// Start starts the command as Run starts virtiofsd and QEMU.
func Start(cmd *exec.Cmd) error {
	return start(cmd)
}

// FirmwareIn is the firmware QEMU loads, each file from the first of the
// folders that has it, as Run binds it into the sandbox when the options
// name none.
func FirmwareIn(dirs []string) ([]string, error) {
	return firmwareIn(dirs)
}

// Libraries are the loader and the libraries of the program, as Run binds
// them into the sandbox when the options name none.
func Libraries(program string) ([]string, error) {
	return libraries(program)
}

// Listed are the loader and the files in the list of the loader.
func Listed(loader string, list []byte) ([]string, error) {
	return listed(loader, list)
}
