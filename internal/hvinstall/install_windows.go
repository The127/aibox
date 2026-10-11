package hvinstall

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/the127/aibox/internal/hvserver"
	"github.com/the127/aibox/internal/hvservice"
	"github.com/the127/aibox/internal/vhd"
)

// ErrNotAdministrator is an install or uninstall by someone who may not
// change the services of the machine.
var ErrNotAdministrator = errors.New("installing or uninstalling the service takes an Administrator: run aibox from an elevated prompt")

// ErrForeignFolder is a folder of the service that someone other than an
// Administrator or SYSTEM made, or one that is a link: whoever made it
// could change what the service boots as SYSTEM.
var ErrForeignFolder = errors.New("the folder of the service is not one an Administrator made")

// folderSDDL is the security of the service's folder: SYSTEM and
// Administrators may change it, and Hyper-V's VMs may read the kernel and
// the root disk they boot from. Whatever ProgramData lets the people of
// the machine do, none of it reaches the folder.
const folderSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FR;;;" + hvservice.VirtualMachines + ")"

// imageKernel and imageRoot are the files of a VM image the service takes.
const (
	imageKernel = "vmlinuz-hyperv"
	imageRoot   = "os.ext4"
)

// Install installs the service of aibox, or brings an installed one up to
// date: the running aibox under Program Files, the kernel and the root
// disk of the image folder in the service's folder, and the service,
// which starts with the machine, started; it returns once the service
// runs. It stops an installed service, and with it its VMs, before it
// changes what the service runs.
func Install(image string) error {
	m, err := connect()
	if err != nil {
		return err
	}

	defer func() { _ = m.Disconnect() }()

	if err := stopService(m); err != nil {
		return err
	}

	exe, err := installExe()
	if err != nil {
		return err
	}

	folder, err := hvserver.Folder()
	if err != nil {
		return err
	}

	if err := makeFolder(folder, isAdministrators); err != nil {
		return err
	}

	if err := placeImage(image, folder); err != nil {
		return err
	}

	if err := registerEventSource(); err != nil {
		return err
	}

	s, err := createService(m, exe)
	if err != nil {
		return err
	}

	defer func() { _ = s.Close() }()

	if err := s.Start(); err != nil {
		return fmt.Errorf("start the service: %w", err)
	}

	// so that an install that returns has a service that runs
	return waitStarted(s.Handle)
}

// Uninstall stops the service, and with it its VMs, and removes it and
// everything Install put in place.
func Uninstall() error {
	m, err := connect()
	if err != nil {
		return err
	}

	defer func() { _ = m.Disconnect() }()

	if err := stopService(m); err != nil {
		return err
	}

	if err := deleteService(m); err != nil {
		return err
	}

	if err := eventlog.Remove(hvserver.ServiceName); err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return fmt.Errorf("unregister the service from the event log: %w", err)
	}

	folder, err := hvserver.Folder()
	if err != nil {
		return err
	}

	dir, err := exeFolder()
	if err != nil {
		return err
	}

	if err := os.RemoveAll(folder); err != nil {
		return fmt.Errorf("remove %s: %w", folder, err)
	}

	return removeAtRestart(dir)
}

// deleteService removes the service, if there is one.
func deleteService(m *mgr.Mgr) error {
	s, err := m.OpenService(hvserver.ServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("open the service: %w", err)
	}

	defer func() { _ = s.Close() }()

	if err := s.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("remove the service: %w", err)
	}

	return nil
}

// ErrAtRestart is a file Uninstall could only have Windows remove when the
// machine next starts: the aibox that uninstalls, as Windows removes no
// program that runs.
var ErrAtRestart = errors.New("removed when Windows next starts, as aibox runs from it")

// removeAtRestart removes the folder, and has Windows remove a file of it
// that it can't when the machine next starts. Such a file, the aibox that
// runs, is moved out of the folder first, which Windows allows of a
// program that runs: an install before the restart would else lose its
// aibox with it.
func removeAtRestart(dir string) error {
	if err := os.RemoveAll(dir); err == nil {
		return nil
	}

	left, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}

	var later []string

	for _, e := range left {
		path := filepath.Join(dir, e.Name())
		if os.Remove(path) == nil {
			continue
		}

		moved, err := moveAside(dir, path)
		if err != nil {
			return err
		}

		later = append(later, moved)
	}

	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}

	if len(later) > 0 {
		return fmt.Errorf("%s: %w", strings.Join(later, ", "), ErrAtRestart)
	}

	return nil
}

// moveAside moves the file in the folder dir to a name of its own beside
// dir, and has Windows remove it there when the machine next starts. A
// program that runs can be moved only on its own drive, which the folder
// that holds dir is.
func moveAside(dir, path string) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dir), "aibox-removed-*-"+filepath.Base(path))
	if err != nil {
		return "", fmt.Errorf("make room for %s: %w", path, err)
	}

	moved := tmp.Name()
	_ = tmp.Close()

	if err := os.Rename(path, moved); err != nil {
		_ = os.Remove(moved)

		return "", fmt.Errorf("move %s out of the way: %w", path, err)
	}

	p, err := windows.UTF16PtrFromString(moved)
	if err != nil {
		return "", err
	}

	if err := windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); err != nil {
		return "", fmt.Errorf("have Windows remove %s when it next starts: %w", moved, err)
	}

	return moved, nil
}

// registerEventSource registers the service with the event log anew: an
// install before may have left the registration half made, and eventlog
// refuses to register a source that is there at all.
func registerEventSource() error {
	if err := eventlog.Remove(hvserver.ServiceName); err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return fmt.Errorf("unregister the service from the event log: %w", err)
	}

	if err := eventlog.InstallAsEventCreate(hvserver.ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		return fmt.Errorf("register the service with the event log: %w", err)
	}

	return nil
}

// CheckAdministrator tells whether aibox runs elevated, as installing the
// service takes, before an install fetches an image for nothing.
func CheckAdministrator() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return ErrNotAdministrator
	}

	return nil
}

// connect connects to the service manager with the rights to install.
func connect() (*mgr.Mgr, error) {
	m, err := mgr.Connect()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, ErrNotAdministrator
	}

	if err != nil {
		return nil, fmt.Errorf("connect to the service manager: %w", err)
	}

	return m, nil
}

// exeFolder is where Install puts aibox for the service to run: under
// Program Files, which only Administrators may change, as the service
// runs it as SYSTEM.
func exeFolder() (string, error) {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", fmt.Errorf("find Program Files: %w", err)
	}

	return filepath.Join(dir, "aibox"), nil
}

// installExe copies the running aibox to its folder under Program Files,
// and returns where it put it.
func installExe() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find aibox: %w", err)
	}

	dir, err := exeFolder()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // it takes the security of Program Files, which the mode does not change on Windows
		return "", fmt.Errorf("make %s: %w", dir, err)
	}

	exe := filepath.Join(dir, "aibox.exe")

	if same, err := samePath(self, exe); err != nil || same {
		return exe, err
	}

	return exe, place(self, exe, nil)
}

// samePath tells whether the two paths name one file, as a re-install
// from the installed aibox has them.
func samePath(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("look at %s: %w", a, err)
	}

	bi, err := os.Stat(b)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("look at %s: %w", b, err)
	}

	return os.SameFile(ai, bi), nil
}

// isAdministrators tells an owner that only Administrators or SYSTEM can
// have made.
func isAdministrators(owner *windows.SID) bool {
	return owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

// makeFolder makes the folder with folderSDDL, or, if it is there, takes
// it only if owned says its owner could only be an Administrator and it
// is no link, and gives it folderSDDL again.
func makeFolder(path string, owned func(*windows.SID) bool) error {
	sd, err := windows.SecurityDescriptorFromString(folderSDDL)
	if err != nil {
		return fmt.Errorf("read the security of the service's folder: %w", err)
	}

	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}

	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}

	err = windows.CreateDirectory(p, sa)
	if err == nil {
		return nil
	}

	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("make %s: %w", path, err)
	}

	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return fmt.Errorf("look at %s: %w", path, err)
	}

	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("%w: %s is a link or no folder", ErrForeignFolder, path)
	}

	have, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read who owns %s: %w", path, err)
	}

	owner, _, err := have.Owner()
	if err != nil {
		return fmt.Errorf("read who owns %s: %w", path, err)
	}

	if !owned(owner) {
		return fmt.Errorf("%w: %s owns %s; remove it and install again", ErrForeignFolder, owner, path)
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read the security of the service's folder: %w", err)
	}

	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("secure %s: %w", path, err)
	}

	return nil
}

// placeImage puts the kernel and the root disk of the image folder in the
// service's folder, the root disk made a VHD. Each takes its place whole,
// but not both at once: an install that fails between them leaves the
// service stopped, and the next install puts both in place.
func placeImage(image, folder string) error {
	if err := place(filepath.Join(image, imageKernel), filepath.Join(folder, hvserver.KernelFile), nil); err != nil {
		return err
	}

	return place(filepath.Join(image, imageRoot), filepath.Join(folder, hvserver.RootFile), vhd.Append)
}

// place copies src to dst, done to the copy before it takes dst's place,
// so that dst is whole, old or new, whenever the service may read it.
func place(src, dst string, done func(string) error) error {
	tmp := dst + ".new"

	if err := copyFile(src, tmp); err != nil {
		return err
	}

	if done != nil {
		if err := done(tmp); err != nil {
			_ = os.Remove(tmp)

			return err
		}
	}

	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("put %s in place: %w", dst, err)
	}

	return nil
}

// copyFile copies src to dst, which takes the security of its folder.
func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // the image the Administrator installs, or aibox itself
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}

	defer func() { _ = in.Close() }()

	out, err := os.Create(dst) //nolint:gosec // a file of the service's folder, or of Program Files
	if err != nil {
		return fmt.Errorf("make %s: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}

	return nil
}

// createService registers the service to run exe, or brings an installed
// one's config up to date.
func createService(m *mgr.Mgr, exe string) (*mgr.Service, error) {
	config := mgr.Config{
		StartType:   mgr.StartAutomatic,
		DisplayName: "aibox",
		Description: "Makes the Hyper-V VMs of aibox for the people who run it.",
	}

	s, err := m.CreateService(hvserver.ServiceName, exe, config, "service", "run")
	if err == nil {
		return s, nil
	}

	if !errors.Is(err, windows.ERROR_SERVICE_EXISTS) {
		return nil, fmt.Errorf("register the service: %w", err)
	}

	s, err = m.OpenService(hvserver.ServiceName)
	if err != nil {
		return nil, fmt.Errorf("open the service: %w", err)
	}

	// as CreateService has it, and as the account it gave, which an
	// Administrator may have changed since
	config.BinaryPathName = syscall.EscapeArg(exe) + " service run"
	config.ServiceType = windows.SERVICE_WIN32_OWN_PROCESS
	config.ServiceStartName = "LocalSystem"

	if err := s.UpdateConfig(config); err != nil {
		_ = s.Close()

		return nil, fmt.Errorf("update the service: %w", err)
	}

	return s, nil
}

// stopService stops the installed service, if there is one, and waits
// until it has, which is once its VMs have ended.
func stopService(m *mgr.Mgr) error {
	s, err := m.OpenService(hvserver.ServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("open the service: %w", err)
	}

	defer func() { _ = s.Close() }()

	// the service manager says the service stopped before its process has
	// let go of aibox.exe, which Install then could not replace, so the
	// wait is for the process too
	process := serviceProcess(s)
	if process != 0 {
		defer func() { _ = windows.CloseHandle(process) }()
	}

	// a service that is starting or stopping already takes no stop, and is
	// waited for all the same
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) && !errors.Is(err, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL) {
		return fmt.Errorf("stop the service: %w", err)
	}

	if err := waitStopped(s.Handle); err != nil {
		return err
	}

	if process != 0 {
		if _, err := windows.WaitForSingleObject(process, windows.INFINITE); err != nil {
			return fmt.Errorf("wait for the process of the service to end: %w", err)
		}
	}

	return nil
}

// serviceProcess is a handle to wait on for the process of the service to
// end, or 0 if it runs none or the handle can't be had, which leaves only
// the service manager's word to wait on.
func serviceProcess(s *mgr.Service) windows.Handle {
	status, err := s.Query()
	if err != nil || status.ProcessId == 0 {
		return 0
	}

	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, status.ProcessId)
	if err != nil {
		return 0
	}

	return process
}

// notifiedCallback is the callback of every wait for a service: the wait
// reads what it needs from the notification, so the callback has nothing
// to do but end the wait, which Windows does as it returns. Go makes only
// so many callbacks, so there is one for them all.
var notifiedCallback = windows.NewCallback(func(uintptr) uintptr { return 0 })

// waitStopped waits until the service has stopped, as Windows tells it.
func waitStopped(service windows.Handle) error {
	_, err := waitFor(service, windows.SERVICE_NOTIFY_STOPPED)
	if err != nil {
		return fmt.Errorf("wait for the service to stop: %w", err)
	}

	return nil
}

// waitStarted waits until the service runs, or has stopped as it started,
// as Windows tells it.
func waitStarted(service windows.Handle) error {
	state, err := waitFor(service, windows.SERVICE_NOTIFY_RUNNING|windows.SERVICE_NOTIFY_STOPPED)
	if err != nil {
		return fmt.Errorf("wait for the service to start: %w", err)
	}

	if state != windows.SERVICE_RUNNING {
		return ErrNotStarted
	}

	return nil
}

// ErrNotStarted is a service that stopped as it started; the event log
// says why.
var ErrNotStarted = errors.New("the service stopped as it started; the Application event log, source aibox, says why")

// waitFor waits until the service is in one of the states of the mask, and
// returns the state it is in.
func waitFor(service windows.Handle, mask uint32) (uint32, error) {
	// Windows tells only the thread that asked, and only while it waits
	// in SleepEx, so the goroutine keeps that thread until then
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	notify := &windows.SERVICE_NOTIFY{Version: windows.SERVICE_NOTIFY_STATUS_CHANGE, NotifyCallback: notifiedCallback}

	if err := windows.NotifyServiceStatusChange(service, mask, notify); err != nil {
		return 0, err
	}

	// a service in one of the states already is told at once
	windows.SleepEx(windows.INFINITE, true)

	if notify.NotificationStatus != 0 {
		return 0, windows.Errno(notify.NotificationStatus)
	}

	return notify.ServiceStatus.CurrentState, nil
}
