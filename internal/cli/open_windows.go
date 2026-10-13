package cli

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// noFollowFlags are none: Windows has no FIFOs and no controlling
// terminals, and openNoFollow refuses a link on its own.
const noFollowFlags = 0

var errLink = errors.New("the path ends in a link")

// shareAll is every kind of sharing, so that a file can be removed while
// aibox has it open, as on Unix.
const shareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

// openNoFollow opens the file at the path with the flags, and refuses a
// link at the end of the path as O_NOFOLLOW does on Unix: it opens the link
// itself, not what it points to, and then sees that it is one. The file is
// opened with every kind of sharing, so that it can be removed while open,
// as on Unix. Its folder still can't be renamed or removed meanwhile.
func openNoFollow(path string, flag int, _ os.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	var access uint32

	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_RDONLY:
		access = windows.GENERIC_READ
	case os.O_WRONLY:
		access = windows.GENERIC_WRITE
	default:
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	}

	var disposition uint32 = windows.OPEN_EXISTING

	switch {
	case flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		disposition = windows.CREATE_NEW
	case flag&os.O_CREATE != 0:
		disposition = windows.OPEN_ALWAYS
	}

	handle, err := windows.CreateFile(name, access, shareAll, nil, disposition, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	return refuseReparsePoint(handle, path)
}

// openInRoot opens the name in the root for reading, and refuses a link at
// its end in the same step, as O_NOFOLLOW does on Unix. os.Root follows a
// link that stays inside the root, and on Windows no flag keeps it from
// that. So os.Root opens the folder of the name, and the last part of the
// name is opened relative to that folder, as the link itself and not what
// it points to.
func openInRoot(root *os.Root, name string) (*os.File, error) {
	dir, base := filepath.Split(filepath.Clean(name))
	if base == "." || base == ".." {
		// the root itself, which is no link in it, or above it, which
		// os.Root refuses
		return root.OpenFile(name, os.O_RDONLY, 0)
	}

	if dir == "" {
		dir = "."
	}

	parent, err := root.Open(dir)
	if err != nil {
		return nil, err
	}

	defer func() { _ = parent.Close() }()

	path := filepath.Join(root.Name(), name)

	object, err := windows.NewNTUnicodeString(base)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: object}
	attrs.Length = uint32(unsafe.Sizeof(attrs))

	var handle windows.Handle

	err = windows.NtCreateFile(&handle, windows.GENERIC_READ|windows.SYNCHRONIZE, &attrs, &windows.IO_STATUS_BLOCK{}, nil, 0,
		shareAll, windows.FILE_OPEN, windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)

	var status windows.NTStatus
	if errors.As(err, &status) {
		// so that errors.Is sees fs.ErrNotExist and the like
		err = status.Errno()
	}

	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	return refuseReparsePoint(handle, path)
}

// refuseReparsePoint is the file of the handle, or an error when the file
// is a link or another reparse point, which closes the handle.
func refuseReparsePoint(handle windows.Handle, path string) (*os.File, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)

		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)

		return nil, &os.PathError{Op: "open", Path: path, Err: errLink}
	}

	return os.NewFile(uintptr(handle), path), nil
}
