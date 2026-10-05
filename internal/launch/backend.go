package launch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/the127/aibox/internal/backend"
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

// CheckImage says whether the folder holds the kernel and the root disk.
func (b Backend) CheckImage(dir string) error {
	_, _, err := imageFiles(dir)

	return err
}

// Run boots the VM of the spec, see the function Run.
func (b Backend) Run(ctx context.Context, spec backend.Spec) error {
	machine, options, err := b.plan(spec)
	if err != nil {
		return err
	}

	return Run(ctx, machine, options)
}

func (b Backend) plan(spec backend.Spec) (vm.Machine, Options, error) {
	kernel, rootfs, err := imageFiles(spec.Image)
	if err != nil {
		return vm.Machine{}, Options{}, err
	}

	shares := []vm.Share{
		{Tag: "project", Dir: spec.Project},
		{Tag: "home", Dir: spec.Home},
	}

	for _, mount := range spec.Mounts {
		shares = append(shares, vm.Share{Tag: mount.Tag, Dir: mount.Host, Guest: mount.Guest})
	}

	machine := vm.Machine{
		Kernel:    kernel,
		Rootfs:    rootfs,
		State:     spec.State,
		MemoryMiB: spec.MemoryMiB,
		CPUs:      spec.CPUs,
		Shares:    shares,
		Shell:     spec.Shell,
		Owner:     &b.Owner,
	}

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

	return machine, options, nil
}

func imageFiles(dir string) (kernel, rootfs string, err error) {
	kernel = filepath.Join(dir, "vmlinuz")
	rootfs = filepath.Join(dir, "os.ext4")

	for _, file := range []string{kernel, rootfs} {
		if _, err := os.Stat(file); err != nil {
			return "", "", fmt.Errorf("%w, build the VM image with just install-image or pass --image", err)
		}
	}

	return kernel, rootfs, nil
}
