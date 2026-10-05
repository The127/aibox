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
	machine, options, err := b.prepare(spec)
	if err != nil {
		return err
	}

	return Run(ctx, machine, options)
}

// prepare creates the state disk once the image is known to be complete,
// and turns the spec into the machine and the options of Run.
func (b Backend) prepare(spec backend.Spec) (vm.Machine, Options, error) {
	kernel, rootfs, err := imageFiles(spec.Image)
	if err != nil {
		return vm.Machine{}, Options{}, err
	}

	if err := createState(spec.State, spec.StateBytes); err != nil {
		return vm.Machine{}, Options{}, err
	}

	shares := []vm.Share{
		{Tag: "project", Dir: spec.Project},
		{Tag: "home", Dir: spec.Home},
	}

	// the guest learns the path of a mount from the kernel command line, so
	// the tag only has to be unique
	for i, mount := range spec.Mounts {
		shares = append(shares, vm.Share{Tag: fmt.Sprintf("mount%d", i), Dir: mount.Host, Guest: mount.Guest})
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

// createState makes the state disk at path with the size, as a sparse file
// that takes up space only as the VM writes to it. An existing disk keeps
// its size, so the size counts on the first run only. An empty file is
// sized again, since a run stopped between creating and sizing leaves one.
func createState(path string, size int64) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600) //nolint:gosec // the path is the project's state disk
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("look at %s: %w", path, err)
	}

	if info.Size() != 0 {
		return nil
	}

	if err := file.Truncate(size); err != nil {
		return fmt.Errorf("size %s: %w", path, err)
	}

	return nil
}
