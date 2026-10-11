package hvserver

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

var procAccessCheck = windows.NewLazySystemDLL("advapi32.dll").NewProc("AccessCheck")

// hresultOf is the HRESULT the Host Compute Service fails a call with for
// the Win32 error.
func hresultOf(err windows.Errno) uint32 {
	const win32Facility = 0x80070000

	return win32Facility | uint32(err) //nolint:gosec // a Win32 error code fits the low 16 bits
}

// stateDisks takes the state disks clients hand over by handle, and lets
// VMs at them with grant and takes that away with revoke, which the tests
// fake.
type stateDisks struct {
	grant, revoke func(id, path string) error
}

// fileIdentity is FILE_ID_INFO, which x/sys/windows lacks: a file whatever
// its names, by the volume it is on and its number there. The number is
// 128 bits, as a file system may need more than the 64 of a file index.
type fileIdentity struct {
	volume uint64
	id     [16]byte
}

// take takes the state disk the client of the pipe holds open under the
// handle, lets the VM of the id at it, and tells its path.
//
// The client sends the number of its handle, and the service copies the
// handle out of the client's process: the copy is of a file the client
// could open, whatever a path might name by the time the service looked.
// The file must be a plain one that the client may write, as the VM
// writes it with the access the service grants. The grant goes by path,
// so the file behind the path is checked to be the one of the handle once
// the grant is made.
func (d stateDisks) take(pipe windows.Handle, id string, handle uint64) (stateDisk, error) {
	h, err := duplicate(pipe, handle)
	if err != nil {
		return stateDisk{}, err
	}

	defer func() { _ = windows.CloseHandle(h) }()

	want, err := diskFile(h)
	if err != nil {
		return stateDisk{}, err
	}

	path, err := pathOf(h)
	if err != nil {
		return stateDisk{}, err
	}

	if err := writable(pipe, h); err != nil {
		return stateDisk{}, err
	}

	if err := d.grant(id, path); err != nil {
		return stateDisk{}, err
	}

	// a task's disk is unlinked while its VM runs, so the path is gone by
	// the time the VM is, and its entry with it
	revoke := func() error {
		var hcsErr *hcs.Error

		err := d.revoke(id, path)
		if errors.As(err, &hcsErr) && hcsErr.HRESULT == hresultOf(windows.ERROR_FILE_NOT_FOUND) {
			return nil
		}

		return err
	}

	if err := same(path, want); err != nil {
		return stateDisk{}, errors.Join(err, revoke())
	}

	return stateDisk{path: path, revoke: revoke}, nil
}

// duplicate copies the handle out of the process of the client of the
// pipe. The copy has the access the client's handle has, which the client
// opens with what the checks take: Windows gives a copy no more than the
// original.
func duplicate(pipe windows.Handle, handle uint64) (windows.Handle, error) {
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(pipe, &pid); err != nil {
		return 0, fmt.Errorf("find the process of the client: %w", err)
	}

	process, err := windows.OpenProcess(windows.PROCESS_DUP_HANDLE, false, pid)
	if err != nil {
		return 0, fmt.Errorf("open the process of the client: %w", err)
	}

	defer func() { _ = windows.CloseHandle(process) }()

	var h windows.Handle
	if err := windows.DuplicateHandle(process, windows.Handle(handle), windows.CurrentProcess(), &h, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return 0, fmt.Errorf("%w: the state disk is not an open file of aibox: %w", hvservice.ErrBadRequest, err)
	}

	return h, nil
}

// diskFile checks that the handle is a plain file on a disk, which Hyper-V
// can attach, and tells which.
func diskFile(h windows.Handle) (fileIdentity, error) {
	typ, err := windows.GetFileType(h)
	if err != nil {
		return fileIdentity{}, fmt.Errorf("look at the state disk: %w", err)
	}

	if typ != windows.FILE_TYPE_DISK {
		return fileIdentity{}, fmt.Errorf("%w: the state disk is no file on a disk", hvservice.ErrBadRequest)
	}

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fileIdentity{}, fmt.Errorf("look at the state disk: %w", err)
	}

	// a link's path would name what it points at, which the client may
	// not write, and could be on another machine
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fileIdentity{}, fmt.Errorf("%w: the state disk is a link", hvservice.ErrBadRequest)
	}

	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return fileIdentity{}, fmt.Errorf("%w: the state disk is a folder", hvservice.ErrBadRequest)
	}

	var identity fileIdentity
	if err := windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity))); err != nil { //nolint:gosec // the struct is handed to the call as the ABI asks
		return fileIdentity{}, fmt.Errorf("tell the state disk from other files: %w", err)
	}

	return identity, nil
}

// pathOf is the path of the file of the handle, as the Host Compute
// Service takes it.
func pathOf(h windows.Handle) (string, error) {
	var buf [windows.MAX_LONG_PATH]uint16

	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", fmt.Errorf("find the path of the state disk: %w", err)
	}

	if n >= uint32(len(buf)) {
		return "", fmt.Errorf("find the path of the state disk: %d characters, more than a path can have", n)
	}

	return hcsPath(windows.UTF16ToString(buf[:n]))
}

// hcsPath is the final path of a file as the Host Compute Service takes
// it: on a drive of this machine, without the prefix that lets a path be
// longer than DOS paths are. A file on another machine is refused: the
// VM's account is of this machine only, and the service, as SYSTEM, is
// not to reach out to others.
func hcsPath(final string) (string, error) {
	rest, ok := strings.CutPrefix(final, `\\?\`)
	if !ok || len(rest) < 3 || rest[1] != ':' || rest[2] != '\\' {
		return "", fmt.Errorf("%w: the state disk is not on a drive of this machine", hvservice.ErrBadRequest)
	}

	return rest, nil
}

// fileAllAccess is FILE_ALL_ACCESS, which x/sys/windows lacks: every
// standard right, and every one of a file's.
const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF

// fileMapping is how the generic rights map to a file's.
var fileMapping = struct{ read, write, execute, all uint32 }{
	windows.FILE_GENERIC_READ, windows.FILE_GENERIC_WRITE, windows.FILE_GENERIC_EXECUTE, fileAllAccess,
}

// privilegeSet is PRIVILEGE_SET with room for one privilege, which is
// more than a check of a file's rights uses.
type privilegeSet struct {
	count, control uint32
	privilege      windows.LUIDAndAttributes
}

// writable checks that the client of the pipe may read and write the file
// of the handle, with the client's own token: the service could open
// anything, and the VM gets to write the file.
func writable(pipe, h windows.Handle) error {
	// the label too, which keeps a client of low integrity from writing
	// what its DACL alone would let it
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the security of the state disk: %w", err)
	}

	return hvservice.AsClient(pipe, func(token windows.Token) error {
		var (
			privileges privilegeSet
			size       = uint32(unsafe.Sizeof(privileges))
			granted    uint32
			allowed    int32
		)

		// the structs are handed to the call as the ABI asks
		r, _, err := procAccessCheck.Call(
			uintptr(unsafe.Pointer(sd)), uintptr(token), windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, //nolint:gosec // see above
			uintptr(unsafe.Pointer(&fileMapping)), uintptr(unsafe.Pointer(&privileges)), uintptr(unsafe.Pointer(&size)), //nolint:gosec // see above
			uintptr(unsafe.Pointer(&granted)), uintptr(unsafe.Pointer(&allowed))) //nolint:gosec // see above
		if r == 0 {
			return fmt.Errorf("check the client's access to the state disk: %w", err)
		}

		if allowed == 0 {
			return fmt.Errorf("%w: you may not write the state disk", hvservice.ErrBadRequest)
		}

		return nil
	})
}

// same checks that the file at the path is the one that was checked: the
// grant went by the path, which someone may have pointed at another file
// meanwhile.
func same(path string, want fileIdentity) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}

	// a link put at the path meanwhile is seen as the link, not followed
	// to wherever it points
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("open the state disk at %s: %w", path, err)
	}

	defer func() { _ = windows.CloseHandle(h) }()

	have, err := diskFile(h)
	if err != nil {
		return err
	}

	if have != want {
		return fmt.Errorf("%w: the state disk at %s changed meanwhile", hvservice.ErrBadRequest, path)
	}

	return nil
}
