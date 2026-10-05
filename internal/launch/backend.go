package launch

import (
	"context"
	"os"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/vm"
)

const (
	qemuProgram      = "qemu-system-x86_64"
	virtiofsdProgram = "/usr/libexec/virtiofsd"
)

// Backend runs the VM on QEMU and virtiofsd, on a Linux host with KVM.
// Owner is the host user the VM user stands for in the shares.
type Backend struct {
	QEMU      string
	Virtiofsd string
	Owner     vm.Owner
}

var _ backend.Backend = Backend{}

// NewBackend returns the Backend for the user running aibox.
func NewBackend() Backend {
	return Backend{
		QEMU:      qemuProgram,
		Virtiofsd: virtiofsdProgram,
		Owner:     vm.Owner{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}, //nolint:gosec // never negative on Linux
	}
}

// Run boots the VM of the spec, see the function Run.
func (b Backend) Run(ctx context.Context, spec backend.Spec) error {
	m, options, err := b.prepare(spec)
	if err != nil {
		return err
	}

	return Run(ctx, m, options)
}

// prepare creates the state disk once the image is known to be complete,
// and turns the spec into the machine and the options of Run.
func (b Backend) prepare(spec backend.Spec) (vm.Machine, Options, error) {
	m, err := machine.Prepare(spec)
	if err != nil {
		return vm.Machine{}, Options{}, err
	}

	m.Owner = &b.Owner

	options := Options{
		QEMU:       b.QEMU,
		Virtiofsd:  b.Virtiofsd,
		Stdin:      spec.Stdin,
		Stdout:     spec.Stdout,
		Stderr:     spec.Stderr,
		ConsoleLog: spec.ConsoleLog,
		Ports:      spec.Ports,
		Proxy:      spec.Proxy,
		Env:        spec.Env,
		NoSandbox:  spec.Unsandboxed,
	}

	return m, options, nil
}
