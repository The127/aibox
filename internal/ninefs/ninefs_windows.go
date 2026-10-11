package ninefs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"

	"github.com/hugelgupf/p9/fsimpl/templatefs"
	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/windows"
)

// Share is a folder of the host the guest mounts by its name, the aname of
// its attach.
type Share struct {
	Name string
	Dir  string
	// ReadOnly refuses every change to the share.
	ReadOnly bool
}

// Owner is who the guest sees as the owner of every file: NTFS keeps no
// Linux owner, and the guest user must own its files for git to use them.
type Owner struct {
	UID uint32
	GID uint32
}

// FS serves shares. It holds each share's folder open from New until
// Close, so the folder can't be swapped for another while it serves.
type FS struct {
	shares map[string]share
	owner  Owner
}

// share is the open folder of a share.
type share struct {
	h        windows.Handle
	readOnly bool
}

// New opens the folders of the shares as the calling user.
func New(shares []Share, owner Owner) (*FS, error) {
	fs := &FS{shares: make(map[string]share, len(shares)), owner: owner}

	for _, s := range shares {
		if err := checkName(s.Name); err != nil {
			_ = fs.Close()

			return nil, fmt.Errorf("the share %q: %w", s.Name, err)
		}

		h, err := openDir(s.Dir)
		if err != nil {
			_ = fs.Close()

			return nil, fmt.Errorf("the share %s: %w", s.Name, err)
		}

		fs.shares[s.Name] = share{h: h, readOnly: s.ReadOnly}
	}

	return fs, nil
}

// Close closes the folders of the shares. Files the guest still has open
// stay open.
func (fs *FS) Close() error {
	var errs []error
	for _, s := range fs.shares {
		errs = append(errs, windows.CloseHandle(s.h))
	}

	fs.shares = nil

	return errors.Join(errs...)
}

// Attach is where the guest starts: a folder that holds the shares by
// their names.
func (fs *FS) Attach() (p9.File, error) {
	return &root{fs: fs}, nil
}

// openDir opens a share's folder to walk from, reparse point or not, as
// the user asked for it.
func openDir(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)

		return 0, fmt.Errorf("look at %s: %w", path, err)
	}

	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = windows.CloseHandle(h)

		return 0, fmt.Errorf("%s: %w", path, windows.ERROR_DIRECTORY)
	}

	return h, nil
}

// checkName refuses a name that is not one entry of a folder: Windows
// would read a backslash as a separator and a colon as a data stream.
func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00") {
		return linux.EINVAL
	}

	return nil
}

// openAt opens the name in the folder of parent, or parent itself again for
// an empty name.
func openAt(parent windows.Handle, name string, access uint32) (windows.Handle, error) {
	return ntCreate(parent, name, access, windows.FILE_OPEN, 0)
}

// ntCreate opens or creates the name in the folder of parent as the
// disposition says. It does not follow a reparse point: a link or junction
// is opened as itself, so nothing can lead out of the share.
func ntCreate(parent windows.Handle, name string, access, disposition, options uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, linux.EINVAL
	}

	oa := windows.OBJECT_ATTRIBUTES{
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	oa.Length = uint32(unsafe.Sizeof(oa))

	var (
		h    windows.Handle
		iosb windows.IO_STATUS_BLOCK
	)

	err = windows.NtCreateFile(&h, access|windows.SYNCHRONIZE, &oa, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition,
		options|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return 0, errno(err)
	}

	return h, nil
}

// root is the folder the guest attaches to, which holds the shares.
type root struct {
	p9.DefaultWalkGetAttr
	templatefs.NotSymlinkFile
	templatefs.NoopRenamed
	templatefs.NotLockable
	templatefs.NilSyncer
	templatefs.NilCloser
	templatefs.XattrUnimplemented
	unchangeable

	fs *FS
}

func (r *root) Walk(names []string) ([]p9.QID, p9.File, error) {
	if len(names) == 0 {
		return nil, &root{fs: r.fs}, nil
	}

	s, ok := r.fs.shares[names[0]]
	if !ok {
		return nil, nil, linux.ENOENT
	}

	h, err := openAt(s.h, "", windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return nil, nil, err
	}

	start := &file{fs: r.fs, h: h, readOnly: s.readOnly}

	qid, _, err := start.info()
	if err != nil {
		_ = start.Close()

		return nil, nil, err
	}

	if len(names) == 1 {
		return []p9.QID{qid}, start, nil
	}

	qids, f, err := start.Walk(names[1:])

	_ = start.Close()

	if err != nil {
		return nil, nil, err
	}

	return append([]p9.QID{qid}, qids...), f, nil
}

func (r *root) GetAttr(p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	return p9.QID{Type: p9.TypeDir}, p9.AttrMaskAll, p9.Attr{
		Mode:  p9.ModeDirectory | 0o555,
		UID:   p9.UID(r.fs.owner.UID),
		GID:   p9.GID(r.fs.owner.GID),
		NLink: 2,
	}, nil
}

func (r *root) Open(mode p9.OpenFlags) (p9.QID, uint32, error) {
	if mode.Mode() != p9.ReadOnly {
		return p9.QID{}, 0, linux.EISDIR
	}

	return p9.QID{Type: p9.TypeDir}, 0, nil
}

func (r *root) ReadAt([]byte, int64) (int, error) { return 0, linux.EISDIR }

func (r *root) Readdir(uint64, uint32) (p9.Dirents, error) { return nil, nil }

func (r *root) StatFS() (p9.FSStat, error) {
	return p9.FSStat{Type: v9fsMagic, BlockSize: 4096, NameLength: 255}, nil
}

// file is a file or folder of a share, held by a handle that may read its
// attributes, until Open adds the access the guest asked for.
type file struct {
	p9.DefaultWalkGetAttr
	templatefs.NoopRenamed
	templatefs.NotLockable
	templatefs.NilSyncer
	templatefs.XattrUnimplemented
	unchangeable

	fs *FS
	h  windows.Handle
	// readOnly is the share's, which refuses every change
	readOnly bool
	open     *os.File
	// entries is the listing of the folder, taken when the guest starts
	// reading it, so that offsets stay where they were
	entries p9.Dirents
}

// v9fsMagic is the f_type Linux reports for a 9p mount.
const v9fsMagic = 0x01021997

func (f *file) info() (p9.QID, windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(f.h, &info); err != nil {
		return p9.QID{}, info, errno(err)
	}

	return qidOf(info.FileAttributes, uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow)), info, nil
}

// qidOf is the QID of a file with the attributes and the file id, which is
// unique on its volume, and walks never cross to another volume.
func qidOf(attributes uint32, id uint64) p9.QID {
	return p9.QID{Type: modeOf(attributes).QIDType(), Path: id}
}

// modeOf is the type and the mode the guest sees. A reparse point is a
// link of some kind, and the walk never follows it.
func modeOf(attributes uint32) p9.FileMode {
	switch {
	case attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
		return p9.ModeSymlink | 0o777
	case attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0:
		return p9.ModeDirectory | 0o755
	default:
		return p9.ModeRegular | 0o644
	}
}

func (f *file) Walk(names []string) ([]p9.QID, p9.File, error) {
	h, err := openAt(f.h, "", windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return nil, nil, err
	}

	cur := &file{fs: f.fs, h: h, readOnly: f.readOnly}
	qids := make([]p9.QID, 0, len(names))

	for _, name := range names {
		if err := checkName(name); err != nil {
			_ = cur.Close()

			return nil, nil, err
		}

		if _, info, err := cur.info(); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
			info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			_ = cur.Close()

			if err != nil {
				return nil, nil, err
			}

			return nil, nil, linux.ENOTDIR
		}

		next, err := openAt(cur.h, name, windows.FILE_READ_ATTRIBUTES)

		_ = cur.Close()

		if err != nil {
			return nil, nil, err
		}

		cur = &file{fs: f.fs, h: next, readOnly: f.readOnly}

		qid, _, err := cur.info()
		if err != nil {
			_ = cur.Close()

			return nil, nil, err
		}

		qids = append(qids, qid)
	}

	return qids, cur, nil
}

func (f *file) GetAttr(p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	qid, info, err := f.info()
	if err != nil {
		return p9.QID{}, p9.AttrMask{}, p9.Attr{}, err
	}

	var basic basicInfo
	if err := windows.GetFileInformationByHandleEx(f.h, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		return p9.QID{}, p9.AttrMask{}, p9.Attr{}, errno(err)
	}

	size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
	attr := p9.Attr{
		Mode:      modeOf(info.FileAttributes),
		UID:       p9.UID(f.fs.owner.UID),
		GID:       p9.GID(f.fs.owner.GID),
		NLink:     p9.NLink(info.NumberOfLinks),
		Size:      size,
		BlockSize: 4096,
		Blocks:    (size + 511) / 512,
	}
	attr.ATimeSeconds, attr.ATimeNanoSeconds = split(basic.LastAccessTime)
	attr.MTimeSeconds, attr.MTimeNanoSeconds = split(basic.LastWriteTime)
	attr.CTimeSeconds, attr.CTimeNanoSeconds = split(basic.ChangeTime)
	attr.BTimeSeconds, attr.BTimeNanoSeconds = split(basic.CreationTime)

	return qid, p9.AttrMaskAll, attr, nil
}

// split turns a time in 100 ns since 1601, as NTFS keeps it, into seconds
// and nanoseconds since 1970.
func split(t int64) (uint64, uint64) {
	ft := windows.Filetime{LowDateTime: uint32(t), HighDateTime: uint32(t >> 32)} //nolint:gosec // a FILETIME is the two halves of the value
	unix := ft.Nanoseconds()
	if unix < 0 {
		return 0, 0
	}

	return uint64(unix / 1e9), uint64(unix % 1e9)
}

func (f *file) Open(mode p9.OpenFlags) (p9.QID, uint32, error) {
	qid, info, err := f.info()
	if err != nil {
		return p9.QID{}, 0, err
	}

	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return p9.QID{}, 0, linux.ELOOP
	}

	if mode.Mode() != p9.ReadOnly {
		return p9.QID{}, 0, linux.EROFS
	}

	access := uint32(windows.FILE_GENERIC_READ)
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		access = windows.FILE_LIST_DIRECTORY | windows.FILE_READ_ATTRIBUTES
	}

	h, err := openAt(f.h, "", access)
	if err != nil {
		return p9.QID{}, 0, err
	}

	f.open = os.NewFile(uintptr(h), "")

	return qid, 0, nil
}

func (f *file) ReadAt(p []byte, offset int64) (int, error) {
	if f.open == nil {
		return 0, linux.EBADF
	}

	n, err := f.open.ReadAt(p, offset)
	if errors.Is(err, io.EOF) {
		return n, nil
	}

	if err != nil {
		return n, errno(err)
	}

	return n, nil
}

func (f *file) Readdir(offset uint64, _ uint32) (p9.Dirents, error) {
	if f.open == nil {
		return nil, linux.EBADF
	}

	if offset == 0 || f.entries == nil {
		entries, err := list(windows.Handle(f.open.Fd()))
		if err != nil {
			return nil, err
		}

		f.entries = entries
	}

	if offset >= uint64(len(f.entries)) {
		return nil, nil
	}

	// the library sends as many as fit in count, and the guest goes on
	// from the offset of the last one it got
	return f.entries[offset:], nil
}

// dirInfo is FILE_ID_BOTH_DIR_INFO up to the name, which follows it.
type dirInfo struct {
	NextEntryOffset uint32
	FileIndex       uint32
	CreationTime    int64
	LastAccessTime  int64
	LastWriteTime   int64
	ChangeTime      int64
	EndOfFile       int64
	AllocationSize  int64
	FileAttributes  uint32
	FileNameLength  uint32
	EaSize          uint32
	ShortNameLength int8
	ShortName       [12]uint16
	FileID          int64
}

const (
	fileIDBothDirectoryInfo        = 10
	fileIDBothDirectoryRestartInfo = 11
)

// list reads the whole folder, without . and .., each entry with the
// offset of the one after it, as a 9p offset is where to go on from.
func list(h windows.Handle) (p9.Dirents, error) {
	var entries p9.Dirents

	// any size works: each call fills the buffer and the next goes on
	// where it stopped
	buf := make([]byte, 64<<10)
	class := uint32(fileIDBothDirectoryRestartInfo)

	for {
		err := windows.GetFileInformationByHandleEx(h, class, &buf[0], uint32(len(buf)))
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return entries, nil
		}

		if err != nil {
			return nil, errno(err)
		}

		class = fileIDBothDirectoryInfo

		for off := 0; ; {
			info := (*dirInfo)(unsafe.Pointer(&buf[off]))
			nameAt := off + int(unsafe.Offsetof(info.FileID)) + 8
			name := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[nameAt])), info.FileNameLength/2))

			if name != "." && name != ".." {
				qid := qidOf(info.FileAttributes, uint64(info.FileID)) //nolint:gosec // a file id is a 64-bit number
				entries = append(entries, p9.Dirent{
					QID:    qid,
					Offset: uint64(len(entries) + 1),
					Type:   qid.Type,
					Name:   name,
				})
			}

			if info.NextEntryOffset == 0 {
				break
			}

			off += int(info.NextEntryOffset)
		}
	}
}

func (f *file) Readlink() (string, error) { return "", linux.ENOSYS }

func (f *file) StatFS() (p9.FSStat, error) {
	return p9.FSStat{Type: v9fsMagic, BlockSize: 4096, NameLength: 255}, nil
}

func (f *file) Close() error {
	var errs []error
	if f.open != nil {
		errs = append(errs, f.open.Close())
		f.open = nil
	}

	if f.h != 0 {
		errs = append(errs, windows.CloseHandle(f.h))
		f.h = 0
	}

	return errors.Join(errs...)
}

// unchangeable refuses every change: the folder that holds the shares is
// the server's, not the host's.
type unchangeable struct{}

func (unchangeable) SetAttr(p9.SetAttrMask, p9.SetAttr) error { return linux.EROFS }

func (unchangeable) WriteAt([]byte, int64) (int, error) { return 0, linux.EROFS }

func (unchangeable) Create(string, p9.OpenFlags, p9.FileMode, p9.UID, p9.GID) (p9.File, p9.QID, uint32, error) {
	return nil, p9.QID{}, 0, linux.EROFS
}

func (unchangeable) Mkdir(string, p9.FileMode, p9.UID, p9.GID) (p9.QID, error) {
	return p9.QID{}, linux.EROFS
}

func (unchangeable) Symlink(string, string, p9.UID, p9.GID) (p9.QID, error) {
	return p9.QID{}, linux.EROFS
}

func (unchangeable) Link(p9.File, string) error { return linux.EROFS }

func (unchangeable) Mknod(string, p9.FileMode, uint32, uint32, p9.UID, p9.GID) (p9.QID, error) {
	return p9.QID{}, linux.EROFS
}

func (unchangeable) Rename(p9.File, string) error { return linux.EROFS }

func (unchangeable) RenameAt(string, p9.File, string) error { return linux.EROFS }

func (unchangeable) UnlinkAt(string, uint32) error { return linux.EROFS }

// basicInfo is FILE_BASIC_INFO, which x/sys/windows lacks.
type basicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
}
