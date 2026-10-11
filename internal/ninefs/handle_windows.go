package ninefs

import (
	"unsafe"

	"github.com/hugelgupf/p9/linux"
	"golang.org/x/sys/windows"
)

// changeAccess is every right of a handle that changes the file. A
// read-only share grants none of them.
const changeAccess = windows.DELETE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
	windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA | windows.WRITE_DAC | windows.WRITE_OWNER

// with opens the name in the folder, or the file itself for an empty name,
// again with the access, for fn. Every change of a file starts here, so a
// read-only share refuses access to change it here.
func (f *file) with(name string, access uint32, fn func(h windows.Handle) error) error {
	if access&changeAccess != 0 && f.share.readOnly {
		return linux.EROFS
	}

	if name != "" {
		if err := checkName(name); err != nil {
			return err
		}
	}

	h, err := openAt(f.h, name, access)
	if err != nil {
		return err
	}

	defer func() { _ = windows.CloseHandle(h) }()

	return fn(h)
}

// setInfo sets the information of the class to v.
func setInfo[T any](h windows.Handle, class uint32, v *T) error {
	return errno(windows.SetFileInformationByHandle(h, class, (*byte)(unsafe.Pointer(v)), uint32(unsafe.Sizeof(*v)))) //nolint:gosec // the call reads v, as the ABI asks
}

// dispose deletes the open file with POSIX semantics: the name goes at
// once even while the file is open elsewhere, as git and build tools
// expect.
func dispose(h windows.Handle, flags uint32) error {
	disposition := windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS | flags

	return setInfo(h, windows.FileDispositionInfoEx, &disposition)
}

// at is where a read or write of a synchronous handle starts. The handle
// still waits for the call to finish.
func at(offset int64) *windows.Overlapped {
	return &windows.Overlapped{Offset: uint32(offset), OffsetHigh: uint32(offset >> 32)} //nolint:gosec // an offset is the two halves of the value
}
