package ninefs

import (
	"errors"
	"testing"

	"github.com/hugelgupf/p9/linux"
	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/windows"
)

func TestTheGuestGetsTheLinuxErrorOfAWindowsOne(t *testing.T) {
	t.Parallel()

	for windowsErr, want := range map[error]linux.Errno{
		windows.ERROR_FILE_NOT_FOUND:         linux.ENOENT,
		windows.STATUS_OBJECT_NAME_NOT_FOUND: linux.ENOENT,
		windows.ERROR_ACCESS_DENIED:          linux.EACCES,
		windows.ERROR_ALREADY_EXISTS:         linux.EEXIST,
		windows.STATUS_OBJECT_NAME_COLLISION: linux.EEXIST,
		// os.ErrExist for Go, which must not win over the mapping
		windows.ERROR_DIR_NOT_EMPTY:        linux.ENOTEMPTY,
		windows.STATUS_DIRECTORY_NOT_EMPTY: linux.ENOTEMPTY,
		// ERROR_ACCESS_DENIED once turned into a Win32 error
		windows.STATUS_FILE_IS_A_DIRECTORY: linux.EISDIR,
		windows.STATUS_NOT_A_DIRECTORY:     linux.ENOTDIR,
		windows.ERROR_NOT_SAME_DEVICE:      linux.EXDEV,
		windows.ERROR_DISK_FULL:            linux.ENOSPC,
		windows.ERROR_INVALID_FUNCTION:     linux.EIO,
	} {
		t.Run(windowsErr.Error(), func(t *testing.T) {
			t.Parallel()

			// act
			got := linux.ExtractErrno(errno(windowsErr))

			// assert
			assert.Equal(t, want, got)
		})
	}
}

func TestAMappedErrorIsItsLinuxErrorAndNotItsWindowsOne(t *testing.T) {
	t.Parallel()

	// act
	err := errno(windows.ERROR_DIR_NOT_EMPTY)

	// assert
	assert.ErrorIs(t, err, linux.ENOTEMPTY)
	assert.False(t, errors.Is(err, windows.ERROR_DIR_NOT_EMPTY))
}
