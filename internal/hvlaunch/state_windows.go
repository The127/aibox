package hvlaunch

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/vhd"
)

// ensureVHD gives the state disk the footer of a fixed VHD, which is what
// Hyper-V attaches, unless it has one. The guest sees the bytes before the
// footer, as it sees the disk on Linux.
func ensureVHD(path string) error {
	if err := vhd.Append(path); err != nil && !errors.Is(err, vhd.ErrAlreadyVHD) {
		return fmt.Errorf("make the state disk a VHD: %w", err)
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
