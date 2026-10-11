package ninefs

import (
	"errors"

	"github.com/hugelgupf/p9/linux"
	"golang.org/x/sys/windows"
)

// errnos are the Linux errors the guest gets for the Windows ones. The
// library maps only a few generic errors itself and turns the rest into
// EIO, which tells a program nothing it could act on.
var errnos = map[windows.Errno]linux.Errno{
	windows.ERROR_FILE_NOT_FOUND:       linux.ENOENT,
	windows.ERROR_PATH_NOT_FOUND:       linux.ENOENT,
	windows.ERROR_INVALID_NAME:         linux.ENOENT,
	windows.ERROR_BAD_PATHNAME:         linux.ENOENT,
	windows.ERROR_ACCESS_DENIED:        linux.EACCES,
	windows.ERROR_SHARING_VIOLATION:    linux.EBUSY,
	windows.ERROR_LOCK_VIOLATION:       linux.EBUSY,
	windows.ERROR_DIRECTORY:            linux.ENOTDIR,
	windows.ERROR_FILE_EXISTS:          linux.EEXIST,
	windows.ERROR_ALREADY_EXISTS:       linux.EEXIST,
	windows.ERROR_DIR_NOT_EMPTY:        linux.ENOTEMPTY,
	windows.ERROR_DISK_FULL:            linux.ENOSPC,
	windows.ERROR_HANDLE_DISK_FULL:     linux.ENOSPC,
	windows.ERROR_WRITE_PROTECT:        linux.EROFS,
	windows.ERROR_NOT_SUPPORTED:        linux.EOPNOTSUPP,
	windows.ERROR_FILENAME_EXCED_RANGE: linux.ENAMETOOLONG,
	windows.ERROR_NOT_SAME_DEVICE:      linux.EXDEV,
	windows.ERROR_INVALID_PARAMETER:    linux.EINVAL,
}

// errno turns an error of a Windows call into the Linux error the guest
// understands, keeping the original for the log.
func errno(err error) error {
	if err == nil {
		return nil
	}

	var status windows.NTStatus
	if errors.As(err, &status) {
		err = status.Errno()
	}

	var winErr windows.Errno
	if errors.As(err, &winErr) {
		if e, ok := errnos[winErr]; ok {
			return &mapped{errno: e, cause: err}
		}
	}

	return &mapped{errno: linux.EIO, cause: err}
}

// mapped is a Windows error as the guest sees it. The library reads the
// errno through errors.As.
type mapped struct {
	errno linux.Errno
	cause error
}

func (m *mapped) Error() string { return m.cause.Error() }

func (m *mapped) As(target any) bool {
	if t, ok := target.(*linux.Errno); ok {
		*t = m.errno

		return true
	}

	return false
}

func (m *mapped) Unwrap() error { return m.cause }
