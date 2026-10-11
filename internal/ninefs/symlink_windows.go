package ninefs

import (
	"errors"
	"strings"
	"unsafe"

	winio "github.com/Microsoft/go-winio"
	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/windows"
)

// nameSurrogate is the bit of a reparse tag that marks a reparse point
// standing for another name: Windows' symlinks and junctions, WSL's
// symlinks, the mount points of volumes. Those are links to the guest, and
// nothing follows them on the host. Other reparse points, such as the
// files OneDrive keeps in the cloud, are files and folders.
const nameSurrogate = 0x20000000

// isLink tells whether a file with the attributes and the reparse tag is a
// link.
func isLink(attributes, tag uint32) bool {
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 && tag&nameSurrogate != 0
}

// reparseTag is the tag of the reparse point the handle is open on.
func reparseTag(h windows.Handle) (uint32, error) {
	var info struct {
		FileAttributes uint32
		ReparseTag     uint32
	}

	err := windows.GetFileInformationByHandleEx(h, windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))) //nolint:gosec // the call fills the struct, as the ABI asks
	if err != nil {
		return 0, errno(err)
	}

	return info.ReparseTag, nil
}

// Readlink is where the link points, which the guest resolves in its own
// file system, never on the host.
func (f *file) Readlink() (string, error) {
	_, info, err := f.info()
	if err != nil {
		return "", err
	}

	if !info.isLink() {
		return "", linux.EINVAL
	}

	buf := make([]byte, windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)

	var n uint32
	if err := windows.DeviceIoControl(f.h, windows.FSCTL_GET_REPARSE_POINT, nil, 0, &buf[0], uint32(len(buf)), &n, nil); err != nil { //nolint:gosec // the largest a reparse point can be
		return "", errno(err)
	}

	target, ok := linkTarget(buf[:n])
	if !ok {
		// another kind of name surrogate, or a link that names nothing
		return "", linux.EINVAL
	}

	return target, nil
}

// Symlink makes a link as WSL does, which needs no privilege, unlike a
// symlink of Windows: an empty file with an LX symlink reparse point that
// holds the target as the guest gave it.
func (f *file) Symlink(target, name string, _ p9.UID, _ p9.GID) (p9.QID, error) {
	if err := f.child(name); err != nil {
		return p9.QID{}, err
	}

	switch {
	case target == "":
		return p9.QID{}, linux.ENOENT
	case strings.ContainsRune(target, 0):
		return p9.QID{}, linux.EINVAL
	case len(target) >= pathMax:
		return p9.QID{}, linux.ENAMETOOLONG
	}

	h, err := ntCreate(f.h, name, windows.FILE_WRITE_DATA|windows.FILE_READ_ATTRIBUTES|windows.DELETE,
		windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE, nil)
	if err != nil {
		return p9.QID{}, err
	}

	defer func() { _ = windows.CloseHandle(h) }()

	rp := winio.EncodeReparsePoint(&winio.ReparsePoint{Target: target, IsLxSymlink: true})

	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &rp[0], uint32(len(rp)), nil, 0, nil, nil); err != nil { //nolint:gosec // a target of the guest, at most what a reparse point holds
		// the empty file is no link, so it goes again
		discard(h)

		if errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
			// a file system without reparse points, as Linux answers for
			// one without symlinks
			return p9.QID{}, linux.EPERM
		}

		return p9.QID{}, errno(err)
	}

	qid, _, err := infoOf(h)

	return qid, err
}

// pathMax is Linux's PATH_MAX, which a target of a link of the guest
// must stay below with its NUL; a reparse point could hold more.
const pathMax = 4096

// discard deletes the file the handle is open on, at once where the
// volume can delete with POSIX semantics and when the handle closes
// where it can't, such as on FAT.
func discard(h windows.Handle) {
	if dispose(h, 0) == nil {
		return
	}

	deleteFile := byte(1)
	_ = setInfo(h, windows.FileDispositionInfo, &deleteFile)
}
