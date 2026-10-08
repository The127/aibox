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

// HostFiles are the libraries and the firmware of the QEMU at the path, as
// Run binds them into the sandbox when the options name none.
func HostFiles(qemu string) (libraries, firmware string) {
	return hostFiles(qemu)
}
