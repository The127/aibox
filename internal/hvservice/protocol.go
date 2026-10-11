// Package hvservice is for the Windows service of aibox and the protocol
// aibox talks to it in. The Host Compute Service serves only
// Administrators, so the service, which runs as SYSTEM, makes the VMs for
// the person who runs aibox. It boots a kernel and a root disk of its own
// and touches no path of that person: aibox serves the folders and builds
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
	}})
	if err != nil {
		panic(err)
	}

	return len(b) + len("\n")
}()

// ErrMessageTooLong is a message longer than either side reads.
var ErrMessageTooLong = errors.New("the message is too long")

// Op is what a request asks of the service.
type Op string

const (
	// OpCreate creates the VM of the connection, which is not started yet.
	OpCreate Op = "create"
	// OpStart starts the VM of the connection.
	OpStart Op = "start"
)

// Request is a message from aibox to the service. A connection is for one
// VM: aibox creates it, starts it, and ends it by closing the connection.
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
}

// Reply is the answer of the service to a request.
type Reply struct {
	// Error says why the service refused the request, or is empty. The
	// service sets it with Refusal.
	Error string `json:"error,omitempty"`
	// RuntimeID and ConsolePipe answer OpCreate: the VM's address for
	// Hyper-V sockets, and the pipe the service connects its console to,
	// which aibox listens on before it starts the VM.
	RuntimeID   string `json:"runtimeId,omitempty"`
	ConsolePipe string `json:"consolePipe,omitempty"`
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

// NewReader is the reader Read takes, with room for the longest message.
func NewReader(r io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(r, maxMessage)
}

// Read reads the next message, one line, into message, which should be a
// new value: fields the line leaves out keep what message had. Read waits
// for the line however long it takes, so a caller that reads from someone
// it doesn't trust sets a deadline. After ErrMessageTooLong the rest of
// the line is still unread, so the stream is of no further use.
func Read(r *bufio.Reader, message any) error {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return ErrMessageTooLong
	}

	if err != nil {
		return fmt.Errorf("receive a message: %w", err)
	}

	if err := json.Unmarshal(line, message); err != nil {
		return fmt.Errorf("decode the message: %w", err)
	}

	return nil
}
