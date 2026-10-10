package machine_test

import (
	"errors"
	"os"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

var procGetCompressedFileSize = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCompressedFileSizeW")

// allocatedBytes is how much space the file takes up on disk, which for a
// sparse file is what was written.
func allocatedBytes(t *testing.T, path string) int64 {
	t.Helper()

	name, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)

	var high uint32

	// INVALID_FILE_SIZE, which a size may end in too, so the error decides
	low, _, err := procGetCompressedFileSize.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&high))) //nolint:gosec // the pointers outlive the call
	if low == 0xFFFFFFFF && !errors.Is(err, windows.ERROR_SUCCESS) {
		require.NoError(t, err)
	}

	return int64(high)<<32 | int64(low&0xFFFFFFFF)
}

// assertPrivate checks nothing: Windows has no permission bits, the file
// inherits the access rights of its folder.
func assertPrivate(*testing.T, os.FileInfo) {}
