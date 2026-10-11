package ninefs

import (
	"os"
	"time"
	"unsafe"

	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/windows"
)

// atRemoveDir is AT_REMOVEDIR, the flag of an unlinkat that removes a
// folder.
const atRemoveDir = 0x200

const (
	// fileRenameInformationEx and fileLinkInformationEx are the NT
	// classes that take a folder handle for the new name, so a rename or
	// link stays relative to handles like every walk.
	fileRenameInformationEx = 65
	fileLinkInformationEx   = 72
)

// accessFor is the access a guest's open asks for. A folder opens to read
// only, as on Linux.
func (f *file) accessFor(mode p9.OpenFlags, dir bool) (uint32, error) {
	if dir {
		if mode.Mode() != p9.ReadOnly {
			return 0, linux.EISDIR
		}

		return windows.FILE_LIST_DIRECTORY | windows.FILE_READ_ATTRIBUTES, nil
	}

	switch mode.Mode() {
	case p9.ReadOnly:
		return windows.FILE_GENERIC_READ, nil
	case p9.WriteOnly:
		if f.readOnly {
			return 0, linux.EROFS
		}

		return windows.FILE_GENERIC_WRITE | windows.FILE_READ_ATTRIBUTES, nil
	case p9.ReadWrite:
		if f.readOnly {
			return 0, linux.EROFS
		}

		return windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE, nil
	default:
		return 0, linux.EINVAL
	}
}

func (f *file) WriteAt(p []byte, offset int64) (int, error) {
	if f.open == nil {
		return 0, linux.EBADF
	}

	n, err := f.open.WriteAt(p, offset)

	return n, errno(err)
}

func (f *file) FSync() error {
	if f.open == nil {
		return nil
	}

	return errno(windows.FlushFileBuffers(windows.Handle(f.open.Fd())))
}

// child checks that the name may be made in the folder.
func (f *file) child(name string) error {
	if f.readOnly {
		return linux.EROFS
	}

	return checkName(name)
}

func (f *file) Create(name string, mode p9.OpenFlags, _ p9.FileMode, _ p9.UID, _ p9.GID) (p9.File, p9.QID, uint32, error) {
	if err := f.child(name); err != nil {
		return nil, p9.QID{}, 0, err
	}

	access, err := f.accessFor(mode, false)
	if err != nil {
		return nil, p9.QID{}, 0, err
	}

	h, err := ntCreate(f.h, name, access, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return nil, p9.QID{}, 0, err
	}

	attrs, err := openAt(h, "", windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		_ = windows.CloseHandle(h)

		return nil, p9.QID{}, 0, err
	}

	created := &file{fs: f.fs, h: attrs, readOnly: f.readOnly, open: os.NewFile(uintptr(h), "")}

	qid, _, err := created.info()
	if err != nil {
		_ = created.Close()

		return nil, p9.QID{}, 0, err
	}

	return created, qid, 0, nil
}

func (f *file) Mkdir(name string, _ p9.FileMode, _ p9.UID, _ p9.GID) (p9.QID, error) {
	if err := f.child(name); err != nil {
		return p9.QID{}, err
	}

	h, err := ntCreate(f.h, name, windows.FILE_READ_ATTRIBUTES, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE)
	if err != nil {
		return p9.QID{}, err
	}

	made := &file{fs: f.fs, h: h}
	defer func() { _ = made.Close() }()

	qid, _, err := made.info()

	return qid, err
}

func (f *file) UnlinkAt(name string, flags uint32) error {
	if err := f.child(name); err != nil {
		return err
	}

	h, err := openAt(f.h, name, windows.DELETE|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}

	gone := &file{fs: f.fs, h: h}
	defer func() { _ = gone.Close() }()

	_, info, err := gone.info()
	if err != nil {
		return err
	}

	dir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0

	switch {
	case dir && flags&atRemoveDir == 0:
		return linux.EISDIR
	case !dir && flags&atRemoveDir != 0:
		return linux.ENOTDIR
	}

	// POSIX semantics: the name goes at once even while the file is open
	// elsewhere, as git and build tools expect
	disposition := uint32(windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS |
		windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE)

	return errno(windows.SetFileInformationByHandle(h, windows.FileDispositionInfoEx,
		(*byte)(unsafe.Pointer(&disposition)), uint32(unsafe.Sizeof(disposition))))
}

func (f *file) RenameAt(oldName string, newDir p9.File, newName string) error {
	if err := f.child(oldName); err != nil {
		return err
	}

	h, err := openAt(f.h, oldName, windows.DELETE|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}

	defer func() { _ = windows.CloseHandle(h) }()

	return renameTo(h, newDir, newName)
}

func (f *file) Rename(newDir p9.File, newName string) error {
	if f.readOnly {
		return linux.EROFS
	}

	h, err := openAt(f.h, "", windows.DELETE|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}

	defer func() { _ = windows.CloseHandle(h) }()

	return renameTo(h, newDir, newName)
}

// renameTo gives the open file the name in the folder, replacing what has
// the name, with POSIX semantics as for an unlink.
func renameTo(h windows.Handle, newDir p9.File, newName string) error {
	dir, ok := newDir.(*file)
	if !ok {
		return linux.EXDEV
	}

	if err := dir.child(newName); err != nil {
		return err
	}

	flags := uint32(windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS |
		windows.FILE_RENAME_IGNORE_READONLY_ATTRIBUTE)

	return setName(h, fileRenameInformationEx, flags, dir.h, newName)
}

func (f *file) Link(target p9.File, newName string) error {
	if err := f.child(newName); err != nil {
		return err
	}

	t, ok := target.(*file)
	if !ok {
		return linux.EXDEV
	}

	return setName(t.h, fileLinkInformationEx, 0, f.h, newName)
}

// setName sets FILE_RENAME_INFORMATION or FILE_LINK_INFORMATION, which
// share a layout: the flags, the folder, and the name after them.
func setName(h windows.Handle, class, flags uint32, dir windows.Handle, name string) error {
	u, err := windows.UTF16FromString(name)
	if err != nil {
		return linux.EINVAL
	}

	u = u[:len(u)-1]

	type header struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
	}

	nameAt := unsafe.Offsetof(header{}.FileNameLength) + unsafe.Sizeof(uint32(0))
	buf := make([]byte, nameAt+uintptr(len(u))*2)

	hdr := (*header)(unsafe.Pointer(&buf[0]))
	hdr.Flags = flags
	hdr.RootDirectory = dir
	hdr.FileNameLength = uint32(len(u) * 2) //nolint:gosec // a name is at most 255 UTF-16 units

	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[nameAt])), len(u)), u)

	var iosb windows.IO_STATUS_BLOCK

	return errno(windows.NtSetInformationFile(h, &iosb, &buf[0], uint32(len(buf)), class)) //nolint:gosec // the buffer holds one name
}

func (f *file) SetAttr(valid p9.SetAttrMask, attr p9.SetAttr) error {
	if f.readOnly {
		return linux.EROFS
	}

	if valid.Permissions {
		// modes come with the next stage of the server
		return linux.EPERM
	}

	if (valid.UID && uint32(attr.UID) != f.fs.owner.UID) || (valid.GID && uint32(attr.GID) != f.fs.owner.GID) {
		return linux.EPERM
	}

	if valid.Size {
		if err := f.truncate(attr.Size); err != nil {
			return err
		}
	}

	if valid.ATime || valid.MTime {
		return f.setTimes(valid, attr)
	}

	return nil
}

func (f *file) truncate(size uint64) error {
	h, err := openAt(f.h, "", windows.FILE_WRITE_DATA)
	if err != nil {
		return err
	}

	defer func() { _ = windows.CloseHandle(h) }()

	end := int64(size) //nolint:gosec // the guest's size, which NTFS refuses beyond what it holds

	return errno(windows.SetFileInformationByHandle(h, windows.FileEndOfFileInfo,
		(*byte)(unsafe.Pointer(&end)), uint32(unsafe.Sizeof(end))))
}

func (f *file) setTimes(valid p9.SetAttrMask, attr p9.SetAttr) error {
	h, err := openAt(f.h, "", windows.FILE_WRITE_ATTRIBUTES)
	if err != nil {
		return err
	}

	defer func() { _ = windows.CloseHandle(h) }()

	// zero leaves a time as it is
	var basic basicInfo

	now := time.Now().UnixNano()

	if valid.ATime {
		basic.LastAccessTime = filetime(now)
		if valid.ATimeNotSystemTime {
			basic.LastAccessTime = filetime(int64(attr.ATimeSeconds)*1e9 + int64(attr.ATimeNanoSeconds)) //nolint:gosec // seconds since 1970 fit
		}
	}

	if valid.MTime {
		basic.LastWriteTime = filetime(now)
		if valid.MTimeNotSystemTime {
			basic.LastWriteTime = filetime(int64(attr.MTimeSeconds)*1e9 + int64(attr.MTimeNanoSeconds)) //nolint:gosec // seconds since 1970 fit
		}
	}

	return errno(windows.SetFileInformationByHandle(h, windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))))
}

// filetime is the time in nanoseconds since 1970 as NTFS keeps it.
func filetime(ns int64) int64 {
	ft := windows.NsecToFiletime(ns)

	return int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
}

func (f *file) Symlink(string, string, p9.UID, p9.GID) (p9.QID, error) {
	// symlinks come with a later stage of the server
	return p9.QID{}, linux.EPERM
}

func (f *file) Mknod(string, p9.FileMode, uint32, uint32, p9.UID, p9.GID) (p9.QID, error) {
	return p9.QID{}, linux.EPERM
}
