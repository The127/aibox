package hvservice

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"unsafe"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// ConsoleSDDL is the security of the pipe the service makes for the
// console of a VM: Hyper-V may connect the VM to it, as SYSTEM or as the
// VM, and no one may open it by name otherwise. aibox reads it by the
// handle the service hands it, which needs no name.
var ConsoleSDDL = fmt.Sprintf("D:P(A;;GA;;;SY)(A;;0x%X;;;%s)", pipeUse, VirtualMachines)

// VirtualMachines is the group of the accounts the VMs of Hyper-V run as.
// The pipe lets the group in rather than the VM's own account, which
// exists only while the VM does, and the pipe is made before it. No VM
// of someone else is sent to the pipe: the service names each VM's pipe
// after that VM, and a pipe has one instance, which Hyper-V connects to.
const VirtualMachines = "S-1-5-83-0"

// consoleBuffer is what the pipe holds of the console between a write of
// the VM and a read of aibox, a page: with none, which 0 asks for, every
// write of the VM waits for a read, and so does the guest at its console.
const consoleBuffer = 4096

// MakeConsole makes the pipe at name for the console of a VM, with the
// security sddl and one instance, which no one can add to: Hyper-V
// connects to it as the VM, and whoever holds the handle takes that
// connection and reads the console. It fails if the pipe is there
// already, whoever made it.
func MakeConsole(name, sddl string) (windows.Handle, error) {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return 0, fmt.Errorf("read the security of the console pipe: %w", err)
	}

	attributes := windows.SecurityAttributes{SecurityDescriptor: sd}
	attributes.Length = uint32(unsafe.Sizeof(attributes))

	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}

	// overlapped, so that the reader can wait for the VM and be stopped;
	// a byte stream, as a console is
	h, err := windows.CreateNamedPipe(n, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, consoleBuffer, consoleBuffer, 0, &attributes)
	if err != nil {
		return 0, fmt.Errorf("make the console pipe: %w", err)
	}

	return h, nil
}

// Console is the console pipe of a VM, from the handle of it the service
// put into this process. Hyper-V connects the VM to it once, which the
// pipe takes only from Listen on: Windows turns a connection to a pipe
// no one listens on away.
type Console struct {
	mu         sync.Mutex
	h          windows.Handle
	event      windows.Handle
	overlapped windows.Overlapped
	// pending tells whether the connection is still to come, which
	// Connect then waits for
	pending bool
	file    io.ReadCloser
	closed  bool
}

// ConsoleOf is the console pipe under the handle, which is the Console's
// to close.
func ConsoleOf(handle uint64) *Console {
	return &Console{h: windows.Handle(handle)}
}

// Listen lets the VM connect to the pipe from now on, which it must
// before the VM starts, and returns at once. The wait is Connect's, with
// an event of Listen's own: the completion port is the reader's to set
// up, once there is one to read.
func (c *Console) Listen() error {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return fmt.Errorf("listen for the console: %w", err)
	}

	c.event = event
	c.overlapped = windows.Overlapped{HEvent: event}

	err = windows.ConnectNamedPipe(c.h, &c.overlapped)

	switch {
	case err == nil, errors.Is(err, windows.ERROR_PIPE_CONNECTED):
		// Hyper-V was there first
		return nil
	case errors.Is(err, windows.ERROR_IO_PENDING):
		c.pending = true

		return nil
	default:
		return fmt.Errorf("listen for the console: %w", err)
	}
}

// Connect waits for Hyper-V to connect the VM, and returns the console to
// read until Hyper-V lets go of it, which it does when the VM is gone.
// Close ends the wait, and Connect fails then.
func (c *Console) Connect() (io.ReadCloser, error) {
	if c.pending {
		var n uint32
		if err := windows.GetOverlappedResult(c.h, &c.overlapped, &n, true); err != nil {
			return nil, fmt.Errorf("wait for the console: %w", err)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, net.ErrClosed
	}

	// winio reads the overlapped handle with a completion port of its
	// own, which it takes over once the wait is done, and lets Close end
	// a read
	file, err := winio.NewOpenFile(c.h)
	if err != nil {
		return nil, fmt.Errorf("take the console pipe: %w", err)
	}

	c.file = file

	return file, nil
}

// Close ends the wait for the VM or the reading of the console, and the
// pipe with it.
func (c *Console) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true

	if c.event != 0 {
		defer func() { _ = windows.CloseHandle(c.event) }()
	}

	if c.file != nil {
		return c.file.Close()
	}

	// Windows ends the wait of a handle that closes
	return windows.CloseHandle(c.h)
}
