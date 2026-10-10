// Package machine prepares on the host what a backend that boots the
// kernel of aibox itself needs, whichever VMM it uses: the files of the
// image, the state disk with its lock, and the vm.Machine of a spec.
package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/filelock"
	"github.com/the127/aibox/internal/vm"
)

// ErrStateBusy is a state disk another run of the project has locked.
var ErrStateBusy = errors.New("another aibox runs this project")

// Image returns the kernel and the root disk in the folder.
func Image(dir string) (kernel, rootfs string, err error) {
	kernel = filepath.Join(dir, "vmlinuz")
	rootfs = filepath.Join(dir, "os.ext4")

	for _, file := range []string{kernel, rootfs} {
		if _, err := os.Stat(file); err != nil {
			return "", "", fmt.Errorf("%w, build the VM image with just install-image or pass --image", err)
		}
	}

	return kernel, rootfs, nil
}

// Prepare creates the state disk once the image is known to be complete,
// and turns the spec into the machine to boot. Owner and the ports are left
// to the backend.
func Prepare(spec backend.Spec) (vm.Machine, error) {
	kernel, rootfs, err := Image(spec.Image)
	if err != nil {
		return vm.Machine{}, err
	}

	if err := createState(spec.State, spec.StateBytes); err != nil {
		return vm.Machine{}, err
	}

	shares := []vm.Share{
		{Tag: "project", Dir: spec.Project},
		{Tag: "home", Dir: spec.Home},
	}

	// a task has its project and home on the state disk. It keeps the
	// mounts, which are where its tools come from.
	if spec.Task != "" {
		shares = []vm.Share{{Tag: "task", Dir: spec.Task, ReadOnly: true}}
	}

	// the guest learns the path of a mount from the kernel command line, so
	// the tag only has to be unique
	for i, mount := range spec.Mounts {
		shares = append(shares, vm.Share{Tag: fmt.Sprintf("mount%d", i), Dir: mount.Host, Guest: mount.Guest})
	}

	return vm.Machine{
		Kernel:    kernel,
		Rootfs:    rootfs,
		State:     spec.State,
		MemoryMiB: spec.MemoryMiB,
		CPUs:      spec.CPUs,
		Shares:    shares,
		Shell:     spec.Shell,
		Task:      spec.Task != "",
		Loopback:  spec.Loopback,
	}, nil
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

	if err := markSparse(file); err != nil {
		return fmt.Errorf("make %s sparse: %w", path, err)
	}

	if err := file.Truncate(size); err != nil {
		return fmt.Errorf("size %s: %w", path, err)
	}

	return nil
}

// LockState opens the state disk for writing and locks it. The lock stays
// with the descriptor, so a second run of the project fails here instead of
// writing to the same file system, for as long as the descriptor or a copy
// of it, such as the one a VMM inherits, is open.
func LockState(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // the path is the project's state disk
	if err != nil {
		return nil, fmt.Errorf("open the state disk: %w", err)
	}

	switch err := filelock.Lock(file, false); {
	case errors.Is(err, filelock.ErrLocked):
		_ = file.Close()

		return nil, fmt.Errorf("%w and has its state disk %s", ErrStateBusy, path)
	case err != nil:
		_ = file.Close()

		return nil, fmt.Errorf("lock the state disk %s: %w", path, err)
	}

	return file, nil
}
