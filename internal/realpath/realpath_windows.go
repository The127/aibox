package realpath

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// Of is the path the file at path is at, with every link followed. The
// file must exist. Windows tells it of the file once it is open, which
// follows every kind of link, as an open of the file by anyone else would.
// The path is absolute, whatever path was given.
func Of(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", &os.PathError{Op: "realpath", Path: path, Err: err}
	}

	// no access to the file is needed for its name, and a folder opens
	// only with backup semantics
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", &os.PathError{Op: "realpath", Path: path, Err: err}
	}

	defer func() { _ = windows.CloseHandle(h) }()

	// the path on its drive, or on its volume for a volume that is
	// mounted on a folder only and has no drive
	final, err := finalPath(h, volumeNameDOS)
	if err != nil {
		final, err = finalPath(h, volumeNameGUID)
	}

	if err != nil {
		return "", &os.PathError{Op: "realpath", Path: path, Err: err}
	}

	return dosPath(final), nil
}

// The volume names GetFinalPathNameByHandle puts in front of a path.
const (
	volumeNameDOS  = 0x0
	volumeNameGUID = 0x1
)

// finalPath is the normalized path of the open file, with its volume
// named as the flags say.
func finalPath(h windows.Handle, flags uint32) (string, error) {
	var buf [windows.MAX_LONG_PATH]uint16

	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), flags)
	if err != nil {
		return "", err
	}

	if n >= uint32(len(buf)) {
		return "", fmt.Errorf("%d characters, more than a path can have", n)
	}

	return windows.UTF16ToString(buf[:n]), nil
}

// dosPath is the final path as it is written elsewhere: without the prefix
// that lets a path be longer than DOS paths are, \\?\C:\x as C:\x and
// \\?\UNC\server\share as \\server\share. A path on a volume without a
// drive keeps it, as \\?\Volume{...}\x: without it, it would be relative.
func dosPath(final string) string {
	if rest, ok := strings.CutPrefix(final, `\\?\UNC\`); ok {
		return `\\` + rest
	}

	if rest, ok := strings.CutPrefix(final, `\\?\`); ok && len(rest) >= 2 && rest[1] == ':' && (len(rest) == 2 || rest[2] == '\\') {
		return rest
	}

	return final
}
