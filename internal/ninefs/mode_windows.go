package ninefs

import (
	"encoding/binary"
	"errors"
	"unsafe"

	winio "github.com/Microsoft/go-winio"
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/windows"
)

// lxMod is the extended attribute WSL keeps the Linux mode of a file in.
// NTFS has no place for a mode of its own, and the guest must find the
// mode it set, or git stops tracking whether a file is executable.
const lxMod = "$LXMOD"

// modeBits are the bits of a mode kept in the attribute. The type comes
// from NTFS, never from the attribute. The p9 library passes on the
// permissions and the sticky bit only, so setuid and setgid never arrive.
const modeBits = 0o7777

// readMode reads the mode the guest set on the file, if it set one. The
// handle must be allowed to read extended attributes.
func readMode(h windows.Handle) (p9.FileMode, bool, error) {
	// FILE_GET_EA_INFORMATION: the offset of the next entry, the length
	// of the name, and the name with its NUL
	query := make([]byte, 5+len(lxMod)+1)
	query[4] = byte(len(lxMod))
	copy(query[5:], lxMod)

	buf := make([]byte, 64)

	var iosb windows.IO_STATUS_BLOCK

	err := windows.NtQueryEaFile(h, &iosb, &buf[0], uint32(len(buf)), true, &query[0], uint32(len(query)), nil, true) //nolint:gosec // 64 bytes and a short query
	if errors.Is(err, windows.STATUS_NO_EAS_ON_FILE) || errors.Is(err, windows.STATUS_NONEXISTENT_EA_ENTRY) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, errno(err)
	}

	eas, err := winio.DecodeExtendedAttributes(buf[:iosb.Information])
	if err != nil {
		return 0, false, errno(err)
	}

	for _, ea := range eas {
		// a query for an attribute the file lacks answers with an empty
		// value rather than an error
		if ea.Name == lxMod && len(ea.Value) == 4 {
			return p9.FileMode(binary.LittleEndian.Uint32(ea.Value)) & modeBits, true, nil
		}
	}

	return 0, false, nil
}

// modeEA is the extended attribute that sets the mode of a file of the
// type, as WSL writes it: the whole st_mode.
func modeEA(fileType, mode p9.FileMode) ([]byte, error) {
	value := make([]byte, 4)
	binary.LittleEndian.PutUint32(value, uint32(fileType.FileType()|mode&modeBits))

	return winio.EncodeExtendedAttributes([]winio.ExtendedAttribute{{Name: lxMod, Value: value}})
}

// writeMode sets the mode of the file, whose handle may write extended
// attributes.
func writeMode(h windows.Handle, fileType, mode p9.FileMode) error {
	ea, err := modeEA(fileType, mode)
	if err != nil {
		return errno(err)
	}

	var iosb windows.IO_STATUS_BLOCK

	return errno(windows.NtSetEaFile(h, &iosb, &ea[0], uint32(len(ea)))) //nolint:gosec // one attribute of a few bytes
}

// defaultMode is the mode of a file the guest never set one on. Windows'
// read-only attribute takes the write bits away.
func defaultMode(attributes uint32) p9.FileMode {
	mode := p9.FileMode(0o644)
	if attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		mode = 0o755
	}

	if attributes&windows.FILE_ATTRIBUTE_READONLY != 0 {
		mode &^= 0o222
	}

	return mode
}

// eaBuffer is what NtCreateFile takes for the extended attributes of a
// new file: a pointer and a length, nothing for none.
func eaBuffer(ea []byte) (uintptr, uint32) {
	if len(ea) == 0 {
		return 0, 0
	}

	return uintptr(unsafe.Pointer(&ea[0])), uint32(len(ea)) //nolint:gosec // one attribute of a few bytes
}
