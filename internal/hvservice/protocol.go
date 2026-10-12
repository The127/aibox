// Package hvservice is for the Windows service of aibox and the protocol
// aibox talks to it in. The Host Compute Service serves only
// Administrators, so the service, which runs as SYSTEM, makes the VMs for
// the person who runs aibox. It boots a kernel and a root disk of its own
// and takes no path from that person: aibox serves the folders and builds
// the kernel command line itself.
package hvservice

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

// PipeName is the named pipe the service listens on.
const PipeName = `\\.\pipe\aibox`

// MaxCmdline is the longest kernel command line: the kernel keeps 2048
// bytes of it on x86, its NUL included, and cuts the rest.
const MaxCmdline = 2047

// maxMessage is the longest message either side reads: the longest
// request, with numbers of the most digits and a command line all of whose
// bytes JSON writes as \u00XX. The service reads the messages of every
// user of the machine, so it must not buffer whatever it is sent. A reply
// fits as Refusal cuts its error.
var maxMessage = func() int {
	b, err := json.Marshal(Request{Op: OpCreate, Create: &Create{
		MemoryMiB: math.MinInt,
		CPUs:      math.MinInt,
		Cmdline:   strings.Repeat("\x00", MaxCmdline),
		State:     math.MaxUint64,
		Network:   &Network{Ports: everyPort(), Loopback: everyPort()},
	}})
	if err != nil {
		panic(err)
	}

	return len(b) + len("\n")
}()

// ErrMessageTooLong is a message longer than either side reads.
var ErrMessageTooLong = errors.New("the message is too long")

// ErrBadMessage is a line that is not a message of the protocol.
var ErrBadMessage = errors.New("not a message of the protocol")

// Op is what a request asks of the service.
type Op string

const (
	// OpCreate creates the VM of the connection, which is not started yet.
	OpCreate Op = "create"
	// OpStart starts the VM of the connection.
	OpStart Op = "start"
	// OpStop ends the VM of the connection, and is answered once it is
	// gone, which closing the connection, which ends it too, can't tell.
	OpStop Op = "stop"
)

// Request is a message from aibox to the service. A connection is for one
// VM: aibox creates it, starts it, and ends it with OpStop or by closing
// the connection.
type Request struct {
	Op Op `json:"op"`
	// Create is the VM to create, for OpCreate.
	Create *Create `json:"create,omitempty"`
}

// Create is the VM aibox asks for. The service adds the kernel and the
// root disk of its own installation.
type Create struct {
	// MemoryMiB is the memory of the VM, in MiB.
	MemoryMiB int `json:"memoryMiB"`
	// CPUs is the number of virtual processors.
	CPUs int `json:"cpus"`
	// Cmdline is the kernel command line, which tells the guest where to
	// find what aibox serves it over Hyper-V sockets.
	Cmdline string `json:"cmdline"`
	// State is the handle of the state disk, open in aibox, as its number.
	// The service copies the handle out of aibox's process rather than
	// take a path: a handle is a file aibox could open, whatever a path
	// might name by the time the service looked. aibox opens the file for
	// its attributes and its security, which the service's checks read,
	// and not for its data, which Hyper-V opens it for, and shares all.
	State uint64 `json:"state"`
	// Network is what the helper of aibox in the VM's container may reach,
	// or nil for a VM whose aibox runs no such helper.
	Network *Network `json:"network,omitempty"`
}

// Network is what the container of a VM may connect to: the TCP ports of
// other hosts, and those on the loopback, which the container may reach
// only through an exemption from the isolation Windows gives it from
// the loopback.
type Network struct {
	Ports    []uint16 `json:"ports,omitempty"`
	Loopback []uint16 `json:"loopback,omitempty"`
}

// Reply is the answer of the service to a request.
type Reply struct {
	// Error says why the service refused the request, or is empty. The
	// service sets it with Refusal.
	Error string `json:"error,omitempty"`
	// RuntimeID and Console answer OpCreate: the VM's address for Hyper-V
	// sockets, and the handle, in aibox's process, of the pipe the service
	// made for the VM's console. Hyper-V connects the VM to the pipe as it
	// starts, and aibox waits for that on the handle, which is its to
	// close. No name of the pipe goes over the protocol: a name could be
	// anyone's, a handle is the service's own.
	RuntimeID string `json:"runtimeId,omitempty"`
	Console   uint64 `json:"console,omitempty"`
	// Container and ContainerSID answer an OpCreate with a Network: the
	// name of the AppContainer the service let reach the network, which
	// aibox makes for its helper, and its SID. The service names it, from
	// a number it makes up and tells only the client, so that no request
	// can point its filters at a container of someone else.
	Container    string `json:"container,omitempty"`
	ContainerSID string `json:"containerSid,omitempty"`
}

// Refusal is the reply that says why the service refused a request, its
// end cut off if the whole of it could be longer than a message: aibox
// would get no reply at all, and so no reason, rather than part of one.
func Refusal(err error) Reply {
	reason := strings.ToValidUTF8(err.Error(), "�")

	// JSON writes no byte longer than \u0000, so a reason of what an error
	// may take of a message, over that, fits whatever its bytes are
	room := (maxMessage - len(`{"error":""}`+"\n")) / len(`\u0000`)
	if len(reason) > room {
		for !utf8.RuneStart(reason[room]) {
			room--
		}

		reason = reason[:room]
	}

	return Reply{Error: reason}
}

// Write writes the message as one line, or returns ErrMessageTooLong for
// one the other side would not read.
func Write(w io.Writer, message any) error {
	b, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode the message: %w", err)
	}

	if len(b) >= maxMessage {
		return ErrMessageTooLong
	}

	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("send the message: %w", err)
	}

	return nil
}

// NewReader is the reader Read takes. Read grows a line as it comes, up to
// the longest message, so the buffer need not hold one.
func NewReader(r io.Reader) *bufio.Reader {
	return bufio.NewReader(r)
}

// Read reads the next message, one line, into message, which should be a
// new value: fields the line leaves out keep what message had. Read waits
// for the line however long it takes; a caller that must not wait closes
// the stream. After ErrMessageTooLong the rest of the line is still
// unread, so the stream is of no further use; after ErrBadMessage it is,
// as the line was read whole.
func Read(r *bufio.Reader, message any) error {
	// the line grows as it comes, rather than a buffer of the longest
	// message for each connection, which every user of the machine opens
	var line []byte

	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > maxMessage {
			return ErrMessageTooLong
		}

		line = append(line, chunk...)

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if err != nil {
			return fmt.Errorf("receive a message: %w", err)
		}

		break
	}

	if err := json.Unmarshal(line, message); err != nil {
		return fmt.Errorf("%w: %w", ErrBadMessage, err)
	}

	return nil
}

// everyPort is every TCP port, the longest list Check takes.
func everyPort() []uint16 {
	ports := make([]uint16, 0, math.MaxUint16)
	for p := math.MaxUint16; p >= 1; p-- {
		ports = append(ports, uint16(p))
	}

	return ports
}
