package hcs

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio/pkg/guid"
	"golang.org/x/sys/windows"
)

// computecore is the DLL of the service. It is loaded on the first call,
// so that a program only pays for it, and only needs it, when it talks to
// the service.
var (
	computecore = windows.NewLazySystemDLL("computecore.dll")

	procCreateOperation            = computecore.NewProc("HcsCreateOperation")
	procCloseOperation             = computecore.NewProc("HcsCloseOperation")
	procCancelOperation            = computecore.NewProc("HcsCancelOperation")
	procWaitForOperationResult     = computecore.NewProc("HcsWaitForOperationResult")
	procCreateComputeSystem        = computecore.NewProc("HcsCreateComputeSystem")
	procOpenComputeSystem          = computecore.NewProc("HcsOpenComputeSystem")
	procCloseComputeSystem         = computecore.NewProc("HcsCloseComputeSystem")
	procStartComputeSystem         = computecore.NewProc("HcsStartComputeSystem")
	procTerminateComputeSystem     = computecore.NewProc("HcsTerminateComputeSystem")
	procGetComputeSystemProperties = computecore.NewProc("HcsGetComputeSystemProperties")
	procEnumerateComputeSystems    = computecore.NewProc("HcsEnumerateComputeSystems")
	procGrantVMAccess              = computecore.NewProc("HcsGrantVmAccess")
	procRevokeVMAccess             = computecore.NewProc("HcsRevokeVmAccess")
)

// defaultTimeout is how long a call of the service may take when the
// context has no deadline.
const defaultTimeout = 2 * time.Minute

// load loads the DLL and finds every call in it, on the first use. A call
// of a procedure that is missing would panic otherwise.
func load() error {
	if err := computecore.Load(); err != nil {
		return fmt.Errorf("load the Host Compute Service: %w", err)
	}

	procs := []*windows.LazyProc{
		procCreateOperation, procCloseOperation, procCancelOperation, procWaitForOperationResult,
		procCreateComputeSystem, procOpenComputeSystem, procCloseComputeSystem,
		procStartComputeSystem, procTerminateComputeSystem, procGetComputeSystemProperties,
		procEnumerateComputeSystems, procGrantVMAccess, procRevokeVMAccess,
	}

	for _, proc := range procs {
		if err := proc.Find(); err != nil {
			return fmt.Errorf("load the Host Compute Service: %w", err)
		}
	}

	return nil
}

// utf16 is s as the service reads it. A string with a NUL in it has no
// such form.
func utf16(s string) (*uint16, error) {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", s, err)
	}

	return p, nil
}

// check turns the HRESULT a call returned into an *Error, with the result
// document of the call if there is one, or nil for a call that succeeded.
func check(op string, hresult uintptr, result string) error {
	code := uint32(hresult) //nolint:gosec // an HRESULT is the low 32 bits of what the call returns

	if code>>31 == 0 {
		return nil
	}

	return &Error{Op: op, HRESULT: code, Result: result}
}

// operation is a pending call of the service. A call that may take a while
// returns at once and finishes in its operation, which hands out the
// result, and the result document with it, once waited for.
type operation uintptr

// newOperation creates an operation, with nothing to call back.
func newOperation() (operation, error) {
	op, _, err := procCreateOperation.Call(0, 0)
	if op == 0 {
		return 0, fmt.Errorf("create an operation: %w", err)
	}

	return operation(op), nil
}

// close frees the operation.
func (op operation) close() {
	_, _, _ = procCloseOperation.Call(uintptr(op))
}

// wait waits for the operation to finish, for as long as the context
// allows, and returns its result document. The document of a call that
// failed goes into its *Error.
func (op operation) wait(ctx context.Context, what string) (string, error) {
	var result *uint16

	hresult, _, _ := procWaitForOperationResult.Call(uintptr(op), uintptr(timeoutOf(ctx)), uintptr(unsafe.Pointer(&result))) //nolint:gosec // the pointer is handed to the call as the ABI asks

	doc := ""
	if result != nil {
		doc = windows.UTF16PtrToString(result)
		// the service allocates the document for the caller to free
		_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(result))) //nolint:gosec // the document is a local allocation of the service
	}

	if err := check(what, hresult, doc); err != nil {
		return "", err
	}

	return doc, nil
}

// timeoutOf is how long a wait may take in the context, in the
// milliseconds the service counts in: until its deadline, or the default
// without one. The largest value stands for no limit, so the wait stops
// one short of it.
func timeoutOf(ctx context.Context) uint32 {
	timeout := defaultTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = max(time.Until(deadline), 0)
	}

	return uint32(min(timeout.Milliseconds(), math.MaxUint32-1)) //nolint:gosec // the value is at least 0 and one short of the largest, by the clamps
}

// run makes an operation, starts a call of the service in it and waits for
// the call to finish, or for the context to end. start makes the call and
// returns its HRESULT. A call whose context ended may still finish.
func run(ctx context.Context, what string, start func(op operation) uintptr) (string, error) {
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s: %w", what, context.Cause(ctx))
	}

	op, err := newOperation()
	if err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}

	if err := check(what, start(op), ""); err != nil {
		op.close()

		return "", err
	}

	type outcome struct {
		doc string
		err error
	}

	done := make(chan outcome, 1)

	go func() {
		doc, err := op.wait(ctx, what)
		done <- outcome{doc, err}
	}()

	select {
	case o := <-done:
		op.close()

		return o.doc, o.err
	case <-ctx.Done():
		// The wait can't be interrupted, and the operation may only be
		// closed once it has returned.
		_, _, _ = procCancelOperation.Call(uintptr(op))

		go func() {
			<-done
			op.close()
		}()

		return "", fmt.Errorf("%s: %w", what, context.Cause(ctx))
	}
}

// System is an open compute system of the service, a VM, from Create or
// Open until Close. Its handle is one of the handles that keep a VM alive
// whose Document says ShouldTerminateOnLastHandleClosed. Its methods may be
// called from several goroutines, so that one can terminate the VM while
// another starts it.
type System struct {
	id string

	// mu keeps Close from closing the handle while a call is waited for.
	// A call whose context ended is no longer waited for, though its
	// operation may still be pending.
	mu     sync.RWMutex
	handle uintptr
}

// Create creates the compute system of the document under the id, which is
// a GUID the caller chooses, and opens it. The VM is not started yet. If
// Create fails, the service may have made the system all the same; only
// ShouldTerminateOnLastHandleClosed ends it then.
func Create(ctx context.Context, id string, doc Document) (*System, error) {
	if err := load(); err != nil {
		return nil, err
	}

	config, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("describe the compute system %s: %w", id, err)
	}

	idp, err := utf16(id)
	if err != nil {
		return nil, err
	}

	configp, err := utf16(string(config))
	if err != nil {
		return nil, err
	}

	s := &System{id: id}

	// the call hands out the handle as it starts, and the operation says
	// whether the system was created
	_, err = run(ctx, "create the compute system "+id, func(op operation) uintptr {
		hresult, _, _ := procCreateComputeSystem.Call(
			uintptr(unsafe.Pointer(idp)),     //nolint:gosec // the strings are handed to the call as the ABI asks
			uintptr(unsafe.Pointer(configp)), //nolint:gosec // and live until it returns
			uintptr(op), 0,
			uintptr(unsafe.Pointer(&s.handle))) //nolint:gosec // the handle comes back through the pointer

		return hresult
	})
	if err != nil {
		_ = s.Close()

		return nil, err
	}

	return s, nil
}

// Open opens the compute system with the id, with every right to it.
func Open(ctx context.Context, id string) (*System, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("open the compute system %s: %w", id, context.Cause(ctx))
	}

	if err := load(); err != nil {
		return nil, err
	}

	idp, err := utf16(id)
	if err != nil {
		return nil, err
	}

	s := &System{id: id}

	hresult, _, _ := procOpenComputeSystem.Call(
		uintptr(unsafe.Pointer(idp)), //nolint:gosec // the string is handed to the call as the ABI asks
		windows.GENERIC_ALL,
		uintptr(unsafe.Pointer(&s.handle))) //nolint:gosec // the handle comes back through the pointer
	if err := check("open the compute system "+id, hresult, ""); err != nil {
		return nil, err
	}

	return s, nil
}

// ID is the id the compute system was created under.
func (s *System) ID() string {
	return s.id
}

// call runs a call of the service that takes the system, an operation and
// no options, and returns its result document.
func (s *System) call(ctx context.Context, what string, proc *windows.LazyProc) (string, error) {
	what += " the compute system " + s.id

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.handle == 0 {
		return "", fmt.Errorf("%s: %w", what, ErrClosed)
	}

	return run(ctx, what, func(op operation) uintptr {
		hresult, _, _ := proc.Call(s.handle, uintptr(op), 0)

		return hresult
	})
}

// Start boots the VM.
func (s *System) Start(ctx context.Context) error {
	_, err := s.call(ctx, "start", procStartComputeSystem)

	return err
}

// Terminate ends the VM at once, as pulling the plug would. A VM that
// ended already is an error of the service.
func (s *System) Terminate(ctx context.Context) error {
	_, err := s.call(ctx, "terminate", procTerminateComputeSystem)

	return err
}

// Properties are the properties of a compute system the service reports.
type Properties struct {
	// ID is the id the system was created under.
	ID string `json:"Id"`
	// Owner names the program the system belongs to.
	Owner string `json:"Owner"`
	// State is the state of the system, such as Created, Running or
	// Stopped.
	State string `json:"State"`
	// RuntimeID is the id of the VM at the hypervisor, which is the one
	// its Hyper-V socket is addressed by.
	RuntimeID guid.GUID `json:"RuntimeId"`
}

// Properties asks the service about the compute system.
func (s *System) Properties(ctx context.Context) (Properties, error) {
	doc, err := s.call(ctx, "look at", procGetComputeSystemProperties)
	if err != nil {
		return Properties{}, err
	}

	var p Properties
	if err := json.Unmarshal([]byte(doc), &p); err != nil {
		return Properties{}, fmt.Errorf("read the properties of the compute system %s: %w: %s", s.id, err, doc)
	}

	return p, nil
}

// Close closes the handle. The VM goes on, unless the handle was the last
// one to a VM whose Document says ShouldTerminateOnLastHandleClosed. The
// service reports no error for it, so Close never returns one, but it is
// an io.Closer. Close waits for the calls in progress, so the contexts of
// those should end first; until then, a waiting Close holds up new calls
// too, Terminate among them.
func (s *System) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.handle == 0 {
		return nil
	}

	_, _, _ = procCloseComputeSystem.Call(s.handle)
	s.handle = 0

	return nil
}

// Summary is what the service lists about a compute system.
type Summary struct {
	// ID is the id the system was created under.
	ID string `json:"Id"`
	// Owner names the program the system belongs to.
	Owner string `json:"Owner"`
	// State is the state of the system, such as Created, Running or
	// Stopped.
	State string `json:"State"`
}

// Enumerate lists the compute systems of the owner, the Owner of their
// Document, which is how a program finds the VMs it left behind.
func Enumerate(ctx context.Context, owner string) ([]Summary, error) {
	if err := load(); err != nil {
		return nil, err
	}

	what := "list the compute systems of " + owner

	q, err := json.Marshal(struct {
		Owners []string `json:"Owners"`
	}{[]string{owner}})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}

	query, err := utf16(string(q))
	if err != nil {
		return nil, err
	}

	doc, err := run(ctx, what, func(op operation) uintptr {
		hresult, _, _ := procEnumerateComputeSystems.Call(
			uintptr(unsafe.Pointer(query)), //nolint:gosec // the string is handed to the call as the ABI asks
			uintptr(op))

		return hresult
	})
	if err != nil {
		return nil, err
	}

	var systems []Summary
	if err := json.Unmarshal([]byte(doc), &systems); err != nil {
		return nil, fmt.Errorf("read the list of compute systems of %s: %w: %s", owner, err, doc)
	}

	return systems, nil
}

// GrantVMAccess lets the VM with the id read the file or the folder at
// the path, by an entry for its account in the ACL of the path. The entry
// stays when the VM is gone, until RevokeVMAccess takes it away.
func GrantVMAccess(id, path string) error {
	return vmAccess("grant the VM "+id+" access to "+path, procGrantVMAccess, id, path)
}

// RevokeVMAccess takes the access to the path away from the VM with the
// id again.
func RevokeVMAccess(id, path string) error {
	return vmAccess("revoke the access of the VM "+id+" to "+path, procRevokeVMAccess, id, path)
}

// vmAccess runs a call of the service that takes the id of a VM and a
// path, and finishes by itself.
func vmAccess(what string, proc *windows.LazyProc, id, path string) error {
	if err := load(); err != nil {
		return err
	}

	idp, err := utf16(id)
	if err != nil {
		return err
	}

	pathp, err := utf16(path)
	if err != nil {
		return err
	}

	hresult, _, _ := proc.Call(
		uintptr(unsafe.Pointer(idp)),   //nolint:gosec // the strings are handed to the call as the ABI asks
		uintptr(unsafe.Pointer(pathp))) //nolint:gosec // and live until it returns

	return check(what, hresult, "")
}
