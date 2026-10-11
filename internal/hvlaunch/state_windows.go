package hvlaunch

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/vhd"
)

// ErrNotDynamic is a state disk that is not a dynamic VHD, which only one
// aibox didn't make on Windows can be.
var ErrNotDynamic = errors.New("not a dynamic VHD, which the state disk is on Windows: remove it, and aibox makes a new one")

// prepareState makes the state disk at path, of size bytes, as a dynamic
// VHD, unless it is there already: Hyper-V attaches VHDs, not raw images,
// and refuses a sparse file, which is how the disk takes up space as it
// fills elsewhere; a dynamic VHD takes up space as it fills by itself. A
// disk that is there is kept whatever its size, as on Linux.
func prepareState(path string, size int64) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || err == nil && info.Size() == 0 {
		if err := vhd.Dynamic(path, size); err != nil {
			return fmt.Errorf("make the state disk: %w", err)
		}

		return nil
	}

	if err != nil {
		return fmt.Errorf("look at the state disk: %w", err)
	}

	is, err := vhd.IsDynamic(path)
	if err != nil {
		return err
	}

	if !is {
		return fmt.Errorf("%s is %w", path, ErrNotDynamic)
	}

	return nil
}

// shareAll lets every other open of the file read, write and delete it:
// aibox holds the state disk for Hyper-V to open, not for itself.
const shareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

// open opens the state disk at path with the access, sharing all.
func open(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

	return windows.CreateFile(p, access, shareAll, nil, windows.OPEN_EXISTING, 0, 0)
}

// checkNotBusy tells whether another VM has the state disk open: Hyper-V
// opens a disk to write without sharing that, so an open to write fails
// while one runs. It is a check, not a lock: a lock of aibox's own would
// keep Hyper-V from the disk too.
func checkNotBusy(path string) error {
	h, err := open(path, windows.GENERIC_WRITE)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return fmt.Errorf("%w and has its state disk %s", machine.ErrStateBusy, path)
	}

	if err != nil {
		return fmt.Errorf("open the state disk: %w", err)
	}

	return windows.CloseHandle(h)
}

// openState opens the state disk as the service takes it: for its
// attributes and its security, which the service checks, and not for its
// data, which Hyper-V opens it for, and which no other open could share
// with.
func openState(path string) (windows.Handle, error) {
	h, err := open(path, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL)
	if err != nil {
		return 0, fmt.Errorf("open the state disk for the service: %w", err)
	}

	return h, nil
}

// unlink removes the name of the state disk while the VM has it open, so
// that the disk is gone with the VM, as on Linux: its name goes now, its
// data when the VM lets go of it. It takes an open of the file to delete
// it, which the VM's own open must share.
func unlink(path string) error {
	h, err := open(path, windows.DELETE)
	if err != nil {
		return fmt.Errorf("open the state disk to remove it: %w", err)
	}

	defer func() { _ = windows.CloseHandle(h) }()

	disposition := uint32(windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS)
	if err := windows.SetFileInformationByHandle(h, windows.FileDispositionInfoEx, (*byte)(unsafe.Pointer(&disposition)), uint32(unsafe.Sizeof(disposition))); err != nil { //nolint:gosec // the struct is handed to the call as the ABI asks
		return fmt.Errorf("remove the state disk: %w", err)
	}

	return nil
}
