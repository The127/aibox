package ninefs

import (
	"errors"
	"fmt"
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
	shares map[string]*share
	owner  Owner
	locks  lockTable[fileKey]
}

// fileKey names a file on the host, whichever name the guest reached it by.
type fileKey struct {
	volume uint32
	id     uint64
}

// share is the open folder of a share.
type share struct {
	h        windows.Handle
	readOnly bool
}

// ErrShareTwice is a share name given to New twice.
var ErrShareTwice = errors.New("two shares of one name")

// New opens the folders of the shares as the calling user.
func New(shares []Share, owner Owner) (*FS, error) {
	fs := &FS{shares: make(map[string]*share, len(shares)), owner: owner}

	for _, s := range shares {
		if err := checkName(s.Name); err != nil {
			_ = fs.Close()

			return nil, fmt.Errorf("the share %q: %w", s.Name, err)
		}

		if _, ok := fs.shares[s.Name]; ok {
			_ = fs.Close()

			return nil, fmt.Errorf("the share %s: %w", s.Name, ErrShareTwice)
		}

		h, err := openDir(s.Dir)
		if err != nil {
			_ = fs.Close()

			return nil, fmt.Errorf("the share %s: %w", s.Name, err)
		}

		fs.shares[s.Name] = &share{h: h, readOnly: s.ReadOnly}
	}

	return fs, nil
}

// Close closes the folders of the shares, once the server serving FS has
// stopped: a walk that started before could use a handle Close freed.
// Files the guest still has open stay open.
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
	return ntCreate(parent, name, access, windows.FILE_OPEN, 0, nil)
}

// ntCreate opens or creates the name in the folder of parent as the
// disposition says. It does not follow a reparse point: a link or junction
// is opened as itself, so nothing can lead out of the share.
func ntCreate(parent windows.Handle, name string, access, disposition, options uint32, ea []byte) (windows.Handle, error) {
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

	eaPtr, eaLen := eaBuffer(ea)

	err = windows.NtCreateFile(&h, access|windows.SYNCHRONIZE, &oa, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition,
		options|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, eaPtr, eaLen)
	if errors.Is(err, windows.STATUS_EAS_NOT_SUPPORTED) && len(ea) > 0 {
		// a volume without extended attributes, such as FAT, keeps no mode;
		// the file shows the default one
		return ntCreate(parent, name, access, disposition, options, nil)
	}

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

	h, err := openAttrs(s.h, "")
	if err != nil {
		return nil, nil, err
	}

	start, qid, err := (&file{fs: r.fs, share: s}).fileOf(h)
	if err != nil {
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

// file is a file or folder of a share. Its handle may only read attributes
// and is what walks start from; Open opens a second one with the access
// the guest asked for.
type file struct {
	p9.DefaultWalkGetAttr
	templatefs.NoopRenamed
	templatefs.NilSyncer
	templatefs.XattrUnimplemented

	fs *FS
	h  windows.Handle
	// share is the share of the file, which a link or rename can't leave
	share *share
	// dir is whether the guest may walk into the file
	dir bool
	// open is the handle Open opened with the access the guest asked for,
	// 0 before; writable says whether it may write, which flushing needs
	open     windows.Handle
	writable bool
	// entries is the listing of the folder, taken when the guest starts
	// reading it, so that offsets stay where they were
	entries p9.Dirents
}

// v9fsMagic is the f_type Linux reports for a 9p mount.
const v9fsMagic = 0x01021997

func (f *file) info() (p9.QID, fileInfo, error) {
	return infoOf(f.h)
}

// infoOf reads what the guest sees of the open file.
func infoOf(h windows.Handle) (p9.QID, fileInfo, error) {
	var info fileInfo
	if err := windows.GetFileInformationByHandle(h, &info.ByHandleFileInformation); err != nil {
		return p9.QID{}, info, errno(err)
	}

	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		tag, err := reparseTag(h)
		if err != nil {
			return p9.QID{}, info, err
		}

		info.tag = tag
	}

	return qidOf(info.FileAttributes, info.tag, uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow)), info, nil
}

// fileInfo is what Windows says about a file, with the tag of its reparse
// point if it is one.
type fileInfo struct {
	windows.ByHandleFileInformation

	tag uint32
}

func (i fileInfo) isLink() bool { return isLink(i.FileAttributes, i.tag) }

// isDir tells whether the guest may walk into the file. A link to a folder
// is a link, which the walk never follows.
func (i fileInfo) isDir() bool {
	return i.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && !i.isLink()
}

// qidOf is the QID of a file with the attributes and the file id. The id
// is unique on a volume, and a guest mounts one share per attach, whose
// walks never cross to another volume.
func qidOf(attributes, tag uint32, id uint64) p9.QID {
	return p9.QID{Type: typeOf(attributes, tag).QIDType(), Path: id}
}

// typeOf is the type the guest sees of a file with the attributes and,
// for a reparse point, the tag.
func typeOf(attributes, tag uint32) p9.FileMode {
	switch {
	case isLink(attributes, tag):
		return p9.ModeSymlink
	case attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0:
		return p9.ModeDirectory
	default:
		return p9.ModeRegular
	}
}

func (f *file) Walk(names []string) ([]p9.QID, p9.File, error) {
	if len(names) == 0 {
		h, err := openAttrs(f.h, "")
		if err != nil {
			return nil, nil, err
		}

		clone, _, err := f.fileOf(h)

		return nil, clone, err
	}

	cur := f
	qids := make([]p9.QID, 0, len(names))

	// every step but the first closes the file of the step before
	done := func() {
		if cur != f {
			_ = cur.Close()
		}
	}

	for _, name := range names {
		if err := checkName(name); err != nil {
			done()

			return nil, nil, err
		}

		if !cur.dir {
			done()

			return nil, nil, linux.ENOTDIR
		}

		h, err := openAttrs(cur.h, name)

		done()

		if err != nil {
			return nil, nil, err
		}

		next, qid, err := f.fileOf(h)
		if err != nil {
			return nil, nil, err
		}

		cur = next
		qids = append(qids, qid)
	}

	return qids, cur, nil
}

// fileOf makes the file of a handle a walk opened, with its QID. It keeps
// whether the file is a folder the guest may walk into, which the next walk
// asks: the type behind an open handle never changes.
func (f *file) fileOf(h windows.Handle) (*file, p9.QID, error) {
	qid, info, err := infoOf(h)
	if err != nil {
		_ = windows.CloseHandle(h)

		return nil, p9.QID{}, err
	}

	return &file{fs: f.fs, h: h, share: f.share, dir: info.isDir()}, qid, nil
}

func (f *file) GetAttr(p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	qid, info, err := f.info()
	if err != nil {
		return p9.QID{}, p9.AttrMask{}, p9.Attr{}, err
	}

	var basic basicInfo
	if err := windows.GetFileInformationByHandleEx(f.h, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil { //nolint:gosec // the call fills the struct, as the ABI asks
		return p9.QID{}, p9.AttrMask{}, p9.Attr{}, errno(err)
	}

	size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
	attr := p9.Attr{
		Mode:      f.mode(info),
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

	if info.isLink() {
		return p9.QID{}, 0, linux.ELOOP
	}

	access, err := f.accessFor(mode, info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0)
	if err != nil {
		return p9.QID{}, 0, err
	}

	h, err := openAt(f.h, "", access)
	if err != nil {
		return p9.QID{}, 0, err
	}

	f.open = h
	f.writable = mode.Mode() != p9.ReadOnly

	return qid, 0, nil
}

func (f *file) ReadAt(p []byte, offset int64) (int, error) {
	if f.open == 0 {
		return 0, linux.EBADF
	}

	// one call at the offset, which the handle never keeps between calls
	var n uint32

	err := windows.ReadFile(f.open, p, &n, at(offset))
	if errors.Is(err, windows.ERROR_HANDLE_EOF) {
		return int(n), nil
	}

	return int(n), errno(err)
}

func (f *file) Readdir(offset uint64, _ uint32) (p9.Dirents, error) {
	if f.open == 0 {
		return nil, linux.EBADF
	}

	if offset == 0 || f.entries == nil {
		entries, err := list(f.open)
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

// list reads the whole folder, without . and .., each entry with the
// offset of the one after it, as a 9p offset is where to go on from.
func list(h windows.Handle) (p9.Dirents, error) {
	var entries p9.Dirents

	// any size works: each call fills the buffer and the next goes on
	// where it stopped
	buf := make([]byte, 64<<10)
	class := uint32(windows.FileIdBothDirectoryRestartInfo)

	for {
		err := windows.GetFileInformationByHandleEx(h, class, &buf[0], uint32(len(buf))) //nolint:gosec // the buffer is 64 KiB
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return entries, nil
		}

		if err != nil {
			return nil, errno(err)
		}

		class = windows.FileIdBothDirectoryInfo

		for off := 0; ; {
			info := (*dirInfo)(unsafe.Pointer(&buf[off])) //nolint:gosec // the call wrote an entry at off, NextEntryOffset said so
			nameAt := off + int(unsafe.Offsetof(info.FileID)) + 8
			name := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[nameAt])), info.FileNameLength/2)) //nolint:gosec // the name follows the entry, FileNameLength bytes long

			if name != "." && name != ".." {
				// on NTFS and ReFS the EA size holds the tag of a reparse
				// point in a listing; elsewhere a link may list as a file,
				// which its attributes and Open still treat as a link
				qid := qidOf(info.FileAttributes, info.EaSize, uint64(info.FileID)) //nolint:gosec // a file id is a 64-bit number
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

func (f *file) StatFS() (p9.FSStat, error) {
	return p9.FSStat{Type: v9fsMagic, BlockSize: 4096, NameLength: 255}, nil
}

func (f *file) Close() error {
	var errs []error
	if f.open != 0 {
		errs = append(errs, windows.CloseHandle(f.open))
		f.open = 0
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

// openAttrs opens the name to walk from and to read the attributes and the
// mode of. A file that lets the user read its attributes but not its
// extended ones still opens, and shows the default mode.
func openAttrs(parent windows.Handle, name string) (windows.Handle, error) {
	h, err := openAt(parent, name, windows.FILE_READ_ATTRIBUTES|windows.FILE_READ_EA)
	if errors.Is(err, linux.EACCES) {
		return openAt(parent, name, windows.FILE_READ_ATTRIBUTES)
	}

	return h, err
}

// mode is the type and the mode of the file with the attributes: the mode
// the guest set, or the default where it set none or it can't be read.
func (f *file) mode(info fileInfo) p9.FileMode {
	attributes := info.FileAttributes
	t := typeOf(attributes, info.tag)
	if t == p9.ModeSymlink {
		return t | 0o777
	}

	if perm, ok, err := readMode(f.h); err == nil && ok {
		return t | perm
	}

	return t | defaultMode(attributes)
}
