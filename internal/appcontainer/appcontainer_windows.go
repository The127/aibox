package appcontainer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The attributes of a new process that put it into a container, give it
// only the handles listed, put it into a job before it runs and keep it
// from starting programs of its own.
const (
	attributeHandleList           = 0x20002
	attributeSecurityCapabilities = 0x20009
	attributeJobList              = 0x2000D
	attributeChildProcessPolicy   = 0x2000E

	childProcessRestricted = 0x01
)

// InternetClient is the capability to connect out over the network.
const InternetClient = "S-1-15-3-1"

var (
	userenv           = windows.NewLazySystemDLL("userenv.dll")
	procCreateProfile = userenv.NewProc("CreateAppContainerProfile")
	procDeleteProfile = userenv.NewProc("DeleteAppContainerProfile")
	procDeriveSID     = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
)

// hresultAlreadyExists is HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS).
const hresultAlreadyExists = 0x800700B7

// DeriveSID is the SID of the container of the name. It depends on the
// name alone, not on the user.
func DeriveSID(name string) (*windows.SID, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}

	var sid *windows.SID

	if r, _, _ := procDeriveSID.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(&sid))); r != 0 { //nolint:gosec // pointers as the API takes them
		return nil, fmt.Errorf("derive the SID of the container %s: HRESULT 0x%08X", name, uint32(r)) //nolint:gosec // an HRESULT is 32 bits
	}

	defer windows.FreeSid(sid) //nolint:errcheck // after the copy, which outlives it

	return sid.Copy()
}

// CreateProfile makes the profile of the container of the name for the
// user, or takes the one there is, and returns its SID.
func CreateProfile(name string) (*windows.SID, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}

	var sid *windows.SID

	r, _, _ := procCreateProfile.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(n)), 0, 0, uintptr(unsafe.Pointer(&sid))) //nolint:gosec // pointers as the API takes them

	switch uint32(r) { //nolint:gosec // an HRESULT is 32 bits
	case 0:
		defer windows.FreeSid(sid) //nolint:errcheck // after the copy, which outlives it

		return sid.Copy()
	case hresultAlreadyExists:
		return DeriveSID(name)
	default:
		return nil, fmt.Errorf("make the profile of the container %s: HRESULT 0x%08X", name, uint32(r)) //nolint:gosec // an HRESULT is 32 bits
	}
}

// DeleteProfile removes the profile of the container of the name.
func DeleteProfile(name string) error {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}

	if r, _, _ := procDeleteProfile.Call(uintptr(unsafe.Pointer(n))); r != 0 { //nolint:gosec // a pointer as the API takes it
		return fmt.Errorf("remove the profile of the container %s: HRESULT 0x%08X", name, uint32(r)) //nolint:gosec // an HRESULT is 32 bits
	}

	return nil
}

// mappings is the key of the user's registry where CreateProfile records
// each profile, under its SID, and moniker the value with its name.
// Windows has no call that lists the profiles of one user:
// NetworkIsolationEnumAppContainers lists those of every user.
const (
	mappings = `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppContainer\Mappings`
	moniker  = "Moniker"
)

// Profiles are the names of the containers the user has a profile of, in
// lower case, as Windows keeps them.
func Profiles() ([]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, mappings, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("list the profiles of containers: %w", err)
	}

	defer func() { _ = k.Close() }()

	sids, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("list the profiles of containers: %w", err)
	}

	names := make([]string, 0, len(sids))

	for _, sid := range sids {
		name, err := profileName(k, sid)
		// a profile removed since the list was read is none, and an entry
		// whose name is no string, which CreateProfile never makes
		if errors.Is(err, registry.ErrNotExist) || errors.Is(err, registry.ErrUnexpectedType) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("read the profile of the container %s: %w", sid, err)
		}

		names = append(names, name)
	}

	return names, nil
}

// profileName is the name of the container of the profile under the SID.
func profileName(parent registry.Key, sid string) (string, error) {
	k, err := registry.OpenKey(parent, sid, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}

	defer func() { _ = k.Close() }()

	name, _, err := k.GetStringValue(moniker)

	return name, err
}

// Job holds the processes started into it, and kills them when it is
// closed, which Windows does when whoever holds it ends, however it ends.
type Job struct {
	h     windows.Handle
	close sync.Once
}

// NewJob makes a job.
func NewJob() (*Job, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("make a job: %w", err)
	}

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			// a program that crashes ends rather than wait for a dialog of
			// Windows nobody sees, as its end is what aibox waits for
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION,
		},
	}

	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil { //nolint:gosec // the struct as the API takes it
		_ = windows.CloseHandle(h)

		return nil, fmt.Errorf("set the limits of a job: %w", err)
	}

	return &Job{h: h}, nil
}

// Close kills the processes of the job.
func (j *Job) Close() error {
	// a second close would close whatever handle has the number by then
	var err error

	j.close.Do(func() { err = windows.CloseHandle(j.h) })

	return err
}

// Spec is what a program is started with.
type Spec struct {
	// Path is the program, and Args its arguments, the first its name.
	Path string
	Args []string
	// Env is its whole environment, as NAME=value: it gets nothing of
	// the one of who starts it, which may hold secrets.
	Env []string
	// Container is the SID of the container it runs in, with the
	// Capabilities, and without a console, or nil for none, which runs it
	// with the rights and the console of who starts it.
	Container    *windows.SID
	Capabilities []*windows.SID
	// Stdin, Stdout and Stderr are its standard handles, and Inherit the
	// other handles it gets, under the same numbers as here. A program
	// gets no other handle.
	Stdin, Stdout, Stderr windows.Handle
	Inherit               []windows.Handle
	// Job is the job it runs in from its start.
	Job *Job
}

// Process is a started program.
type Process struct {
	h     windows.Handle
	Pid   uint32
	close sync.Once
}

// ErrNoJob is a spec without a job, whose program would outlive aibox.
var ErrNoJob = errors.New("a program to start needs a job")

// Start starts the program of the spec.
func Start(s Spec) (*Process, error) {
	if s.Job == nil {
		return nil, ErrNoJob
	}

	if s.Container == nil && len(s.Capabilities) > 0 {
		return nil, ErrNoContainer
	}

	args := s.Args
	if len(args) == 0 {
		args = []string{s.Path}
	}

	env := s.Env

	dir := ""

	if s.Container != nil {
		folder, err := containerFolder(s.Container)
		if err != nil {
			return nil, err
		}

		// the temporary folder of the user is none the container may
		// write, and Go would fall back to the one of Windows
		temp := filepath.Join(folder, "AC", "Temp")
		env = append(append([]string(nil), env...), "TEMP="+temp, "TMP="+temp)
		dir = folder
	}

	path, err := windows.UTF16PtrFromString(s.Path)
	if err != nil {
		return nil, err
	}

	commandLine, err := windows.UTF16PtrFromString(makeCommandLine(args))
	if err != nil {
		return nil, err
	}

	block, err := environment(env)
	if err != nil {
		return nil, err
	}

	// rather than the folder of who starts it, which the container could
	// not open, and whose name is the user's
	var cwd *uint16
	if dir != "" {
		if cwd, err = windows.UTF16PtrFromString(dir); err != nil {
			return nil, err
		}
	}

	// the handles stay inheritable only while the program is made, so that
	// no other program made meanwhile without a list of handles gets them
	starting.Lock()
	defer starting.Unlock()

	handles := inheritList(s)

	restore, err := inheritable(handles)
	if err != nil {
		return nil, err
	}
	defer restore()

	attributes, release, err := s.attributes(handles)
	if err != nil {
		return nil, err
	}
	defer release()

	si := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = s.Stdin, s.Stdout, s.Stderr

	var pi windows.ProcessInformation

	// without a list of handles, a program that may inherit gets every
	// inheritable handle of this process
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	// a program in a container gets no console: on aibox's it could read
	// what the person's shell showed, or type into it
	if s.Container != nil {
		flags |= windows.DETACHED_PROCESS
	}

	if err := windows.CreateProcess(path, commandLine, nil, nil, len(handles) > 0, flags, block, cwd, &si.StartupInfo, &pi); err != nil {
		return nil, fmt.Errorf("start %s: %w", s.Path, err)
	}

	_ = windows.CloseHandle(pi.Thread)

	return &Process{h: pi.Process, Pid: pi.ProcessId}, nil
}

// starting keeps the handles of one start inheritable at a time.
var starting sync.Mutex

// ErrNoContainer is a spec with capabilities but no container, which
// would run its program with all the rights of who starts it.
var ErrNoContainer = errors.New("capabilities need a container")

// inheritable makes the handles inheritable, and returns what makes them
// as they were.
func inheritable(handles []windows.Handle) (func(), error) {
	var made []windows.Handle

	restore := func() {
		for _, h := range made {
			_ = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, 0)
		}
	}

	for _, h := range handles {
		var flags uint32
		if r, _, err := procGetHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags))); r == 0 { //nolint:gosec // a pointer as the API takes it
			restore()

			return nil, fmt.Errorf("look at a handle of the program: %w", err)
		}

		if flags&windows.HANDLE_FLAG_INHERIT != 0 {
			continue
		}

		if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			restore()

			return nil, fmt.Errorf("let the program have a handle: %w", err)
		}

		made = append(made, h)
	}

	return restore, nil
}

var (
	procGetHandleInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")
	procFolderPath           = userenv.NewProc("GetAppContainerFolderPath")
	ole32                    = windows.NewLazySystemDLL("ole32.dll")
	procCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")
)

// containerFolder is the folder of the profile of the container, the one
// it may write in.
func containerFolder(sid *windows.SID) (string, error) {
	s, err := windows.UTF16PtrFromString(sid.String())
	if err != nil {
		return "", err
	}

	var path *uint16

	if r, _, _ := procFolderPath.Call(uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(&path))); r != 0 { //nolint:gosec // pointers as the API takes them
		return "", fmt.Errorf("find the folder of the container: HRESULT 0x%08X", uint32(r)) //nolint:gosec // an HRESULT is 32 bits
	}

	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(path))) //nolint:errcheck,gosec // the API's memory, given back as it says

	return windows.UTF16PtrToString(path), nil
}

// attributes are the attributes of the new process, which gets only the
// handles; they must be inheritable.
func (s Spec) attributes(handles []windows.Handle) (*windows.ProcThreadAttributeListContainer, func(), error) {
	count := uint32(3)
	if s.Container != nil {
		count++
	}

	list, err := windows.NewProcThreadAttributeList(count)
	if err != nil {
		return nil, nil, err
	}

	release := func() { list.Delete() }

	if len(handles) > 0 {
		if err := list.Update(attributeHandleList, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil { //nolint:gosec // the list as the API takes it
			release()

			return nil, nil, fmt.Errorf("give the program its handles: %w", err)
		}
	}

	jobs := []windows.Handle{s.Job.h}
	if err := list.Update(attributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs[0])); err != nil { //nolint:gosec // the list as the API takes it
		release()

		return nil, nil, fmt.Errorf("put the program into the job: %w", err)
	}

	policy := uint32(childProcessRestricted)
	if err := list.Update(attributeChildProcessPolicy, unsafe.Pointer(&policy), unsafe.Sizeof(policy)); err != nil { //nolint:gosec // the value as the API takes it
		release()

		return nil, nil, fmt.Errorf("keep the program from starting others: %w", err)
	}

	if s.Container != nil {
		caps := make([]windows.SIDAndAttributes, len(s.Capabilities))
		for i, c := range s.Capabilities {
			caps[i] = windows.SIDAndAttributes{Sid: c, Attributes: windows.SE_GROUP_ENABLED}
		}

		sc := &securityCapabilities{appContainerSid: s.Container, count: uint32(len(caps))} //nolint:gosec // a few capabilities
		if len(caps) > 0 {
			sc.capabilities = &caps[0]
		}

		if err := list.Update(attributeSecurityCapabilities, unsafe.Pointer(sc), unsafe.Sizeof(*sc)); err != nil { //nolint:gosec // the struct as the API takes it
			release()

			return nil, nil, fmt.Errorf("put the program into the container: %w", err)
		}

		// the list points at them until the process is made
		previous := release
		release = func() {
			previous()
			runtime.KeepAlive(caps)
			runtime.KeepAlive(sc)
		}
	}

	return list, release, nil
}

// securityCapabilities is SECURITY_CAPABILITIES.
type securityCapabilities struct {
	appContainerSid *windows.SID
	capabilities    *windows.SIDAndAttributes
	count           uint32
	reserved        uint32
}

// inheritList are the handles the program gets, each once, and only real
// ones: a standard handle may be none.
func inheritList(s Spec) []windows.Handle {
	var handles []windows.Handle

	for _, h := range append([]windows.Handle{s.Stdin, s.Stdout, s.Stderr}, s.Inherit...) {
		if h == 0 || h == windows.InvalidHandle {
			continue
		}

		seen := false

		for _, have := range handles {
			seen = seen || have == h
		}

		if !seen {
			handles = append(handles, h)
		}
	}

	return handles
}

// environment is the environment as CreateProcess takes it: each
// NAME=value ending in a NUL, and the whole in another.
func environment(env []string) (*uint16, error) {
	var block []uint16

	for _, v := range env {
		u, err := windows.UTF16FromString(v)
		if err != nil {
			return nil, err
		}

		block = append(block, u...)
	}

	block = append(block, 0)
	if len(env) == 0 {
		block = append(block, 0)
	}

	return &block[0], nil
}

// makeCommandLine quotes the arguments as the C runtime and Go parse them.
func makeCommandLine(args []string) string {
	var line []byte

	for i, a := range args {
		if i > 0 {
			line = append(line, ' ')
		}

		line = append(line, syscall.EscapeArg(a)...)
	}

	return string(line)
}

// Wait waits for the program to end and returns its exit code.
func (p *Process) Wait() (uint32, error) {
	if _, err := windows.WaitForSingleObject(p.h, windows.INFINITE); err != nil {
		return 0, err
	}

	var code uint32
	if err := windows.GetExitCodeProcess(p.h, &code); err != nil {
		return 0, err
	}

	return code, nil
}

// Kill ends the program, unless it ended already.
func (p *Process) Kill() error {
	err := windows.TerminateProcess(p.h, 1)

	// Windows refuses to end a program that ended
	var code uint32
	if err != nil && windows.GetExitCodeProcess(p.h, &code) == nil && code != stillActive {
		return nil
	}

	return err
}

// stillActive is the exit code of a program that runs.
const stillActive = 259

// Close lets go of the process, which runs on in its job. Wait and Kill
// must not come after it.
func (p *Process) Close() error {
	// a second close would close whatever handle has the number by then
	var err error

	p.close.Do(func() { err = windows.CloseHandle(p.h) })

	return err
}

// SystemEnv are the variables of this process every program needs to run
// on Windows, which name no secret: where Windows is, the folders of its
// programs, and LOCALAPPDATA, without which Windows finds no folder of
// the container and starts nothing in it.
func SystemEnv() []string {
	var env []string

	for _, name := range []string{"SystemRoot", "SystemDrive", "windir", "PATHEXT", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "LOCALAPPDATA"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}

	return env
}
