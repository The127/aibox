package machine

import (
	"os"

	"golang.org/x/sys/windows"
)

// markSparse makes the file sparse, so that sizing it takes up no space.
// NTFS allocates the whole size of a plain file.
func markSparse(file *os.File) error {
	var returned uint32

	return windows.DeviceIoControl(windows.Handle(file.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &returned, nil)
}
