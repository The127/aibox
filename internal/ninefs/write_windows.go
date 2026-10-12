package ninefs

import (
	"errors"
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
		if f.share.readOnly {
			return 0, linux.EROFS
		}

		return windows.FILE_GENERIC_WRITE | windows.FILE_READ_ATTRIBUTES, nil
	case p9.ReadWrite:
		if f.share.readOnly {
			return 0, linux.EROFS
		}

		return windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE, nil
	default:
		return 0, linux.EINVAL
	}
}

func (f *file) WriteAt(p []byte, offset int64) (int, error) {
	if f.open == 0 {
		return 0, linux.EBADF
	}

	// as for ReadAt: -1 would append
	if offset < 0 {
		return 0, linux.EINVAL
	}

	var n uint32

	err := windows.WriteFile(f.open, p, &n, at(offset))

	return int(n), errno(err)
}

func (f *file) FSync() error {
	// Windows flushes only what a handle may write, and Linux syncs a file
	// or folder opened to read as well, where there is nothing to flush
	if f.open == 0 || !f.writable {
		return nil
	}

	return errno(windows.FlushFileBuffers(f.open))
}

// child checks that the name may be made in the folder.
func (f *file) child(name string) error {
	if f.share.readOnly {
		return linux.EROFS
	}

	return checkName(name)
}

func (f *file) Create(name string, mode p9.OpenFlags, perm p9.FileMode, _ p9.UID, _ p9.GID) (p9.File, p9.QID, uint32, error) {
	if err := f.child(name); err != nil {
		return nil, p9.QID{}, 0, err
	}

	access, err := f.accessFor(mode, false)
	if err != nil {
		return nil, p9.QID{}, 0, err
	}

	ea, err := modeEA(p9.ModeRegular, perm)
	if err != nil {
		return nil, p9.QID{}, 0, errno(err)
	}

	h, err := ntCreate(f.h, name, access, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE, ea)
	if errors.Is(err, linux.EISDIR) {
		// the name is taken, by a folder
		return nil, p9.QID{}, 0, linux.EEXIST
	}

	if err != nil {
		return nil, p9.QID{}, 0, err
	}

	attrs, err := openAttrs(h, "")
	if err != nil {
		_ = windows.CloseHandle(h)

		return nil, p9.QID{}, 0, err
	}

	created := &file{fs: f.fs, h: attrs, share: f.share, open: h, writable: mode.Mode() != p9.ReadOnly}

	qid, _, err := created.info()
	if err != nil {
		_ = created.Close()

		return nil, p9.QID{}, 0, err
	}

	return created, qid, 0, nil
}

func (f *file) Mkdir(name string, perm p9.FileMode, _ p9.UID, _ p9.GID) (p9.QID, error) {
	if err := f.child(name); err != nil {
		return p9.QID{}, err
	}

	ea, err := modeEA(p9.ModeDirectory, perm)
	if err != nil {
		return p9.QID{}, errno(err)
	}

	h, err := ntCreate(f.h, name, windows.FILE_READ_ATTRIBUTES, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE, ea)
	if err != nil {
		return p9.QID{}, err
	}

	defer func() { _ = windows.CloseHandle(h) }()

	qid, _, err := infoOf(h)

	return qid, err
}

func (f *file) UnlinkAt(name string, flags uint32) error {
	return f.with(name, windows.DELETE|windows.FILE_READ_ATTRIBUTES, func(h windows.Handle) error {
		_, info, err := infoOf(h)
		if err != nil {
			return err
		}

		switch dir := info.isDir(); {
		case dir && flags&atRemoveDir == 0:
			return linux.EISDIR
		case !dir && flags&atRemoveDir != 0:
			return linux.ENOTDIR
		}

		return dispose(h, windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE)
	})
}

func (f *file) RenameAt(oldName string, newDir p9.File, newName string) error {
	return f.with(oldName, windows.DELETE|windows.FILE_READ_ATTRIBUTES, func(h windows.Handle) error {
		return renameTo(h, f.share, newDir, newName)
	})
}

func (f *file) Rename(newDir p9.File, newName string) error {
	return f.withSelf(windows.DELETE|windows.FILE_READ_ATTRIBUTES, func(h windows.Handle) error {
		return renameTo(h, f.share, newDir, newName)
	})
}

// renameTo gives the open file of the share the name in the folder,
// replacing what has the name, with POSIX semantics as for an unlink. A
// share is a mount of its own in the guest, so as on Linux nothing moves
// between shares; a writable share could otherwise take a file of a
// read-only one.
func renameTo(h windows.Handle, from *share, newDir p9.File, newName string) error {
	dir, ok := newDir.(*file)
	if !ok || dir.share != from {
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

	// as for a rename: a link from a read-only share would make its file
	// writable through the new name
	t, ok := target.(*file)
	if !ok || t.share != f.share {
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

	// FILE_RENAME_INFORMATION and FILE_LINK_INFORMATION, whose name runs on
	// past the one character the structure declares
	type header struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
		FileName       [1]uint16
	}

	// Windows refuses a buffer shorter than the structure with its padding,
	// and a name of one character makes a shorter one
	nameAt := unsafe.Offsetof(header{}.FileName)
	buf := make([]byte, max(nameAt+uintptr(len(u))*2, unsafe.Sizeof(header{})))

	hdr := (*header)(unsafe.Pointer(&buf[0])) //nolint:gosec // the buffer starts with the header
	hdr.Flags = flags
	hdr.RootDirectory = dir
	hdr.FileNameLength = uint32(len(u) * 2) //nolint:gosec // a name is at most 255 UTF-16 units

	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[nameAt])), len(u)), u) //nolint:gosec // the buffer has room for the name after the header

	var iosb windows.IO_STATUS_BLOCK

	return errno(windows.NtSetInformationFile(h, &iosb, &buf[0], uint32(len(buf)), class)) //nolint:gosec // the buffer holds one name
}

func (f *file) SetAttr(valid p9.SetAttrMask, attr p9.SetAttr) error {
	if f.share.readOnly {
		return linux.EROFS
	}

	// refused before anything changes, so a refused setattr changes nothing
	if (valid.UID && uint32(attr.UID) != f.fs.owner.UID) || (valid.GID && uint32(attr.GID) != f.fs.owner.GID) {
		return linux.EPERM
	}

	if valid.Permissions {
		if err := f.chmod(attr.Permissions); err != nil {
			return err
		}
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
	return f.withSelf(windows.FILE_WRITE_DATA, func(h windows.Handle) error {
		end := int64(size) //nolint:gosec // a size beyond int64 turns negative, which NTFS refuses

		return setInfo(h, windows.FileEndOfFileInfo, &end)
	})
}

func (f *file) setTimes(valid p9.SetAttrMask, attr p9.SetAttr) error {
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

	return f.withSelf(windows.FILE_WRITE_ATTRIBUTES, func(h windows.Handle) error {
		return setInfo(h, windows.FileBasicInfo, &basic)
	})
}

// filetime is the time in nanoseconds since 1970 as NTFS keeps it.
func filetime(ns int64) int64 {
	ft := windows.NsecToFiletime(ns)

	return int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
}

func (f *file) Mknod(string, p9.FileMode, uint32, uint32, p9.UID, p9.GID) (p9.QID, error) {
	return p9.QID{}, linux.EPERM
}

func (f *file) chmod(perm p9.FileMode) error {
	_, info, err := f.info()
	if err != nil {
		return err
	}

	return f.withSelf(windows.FILE_WRITE_EA, func(h windows.Handle) error {
		return writeMode(h, typeOf(info.FileAttributes, info.tag), perm)
	})
}
