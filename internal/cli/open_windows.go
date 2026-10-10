package cli

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// noFollowFlags are none: Windows has no FIFOs and no controlling
// terminals, and openNoFollow refuses a link on its own.
const noFollowFlags = 0

var errLink = errors.New("the path ends in a link")

// openNoFollow opens the file at the path with the flags, and refuses a
// link at the end of the path as O_NOFOLLOW does on Unix: it opens the link
// itself, not what it points to, and then sees that it is one. The file is
// opened with every kind of sharing, so that its folder can still be
// renamed and it can be removed, as on Unix.
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
	if flag&os.O_CREATE != 0 {
		disposition = windows.OPEN_ALWAYS
	}

	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

	handle, err := windows.CreateFile(name, access, share, nil, disposition, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

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

// refuseLink fails when the name in the root is a link. os.Root follows a
// link that stays inside the root, and on Windows no flag keeps it from
// that, so the check comes before the open.
func refuseLink(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return &os.PathError{Op: "open", Path: name, Err: errLink}
	}

	return nil
}
