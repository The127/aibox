// Package backend says what a run of the aibox VM needs and what runs it.
// The packages below implement Backend for one kind of host, and the cli
// picks one without knowing which.
package backend

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/the127/aibox/internal/proxy"
)

// Backend runs a VM for a Spec and returns when the command in it has
// ended. It returns an *ExitError when that command ended with a code other
// than 0.
type Backend interface {
	Run(ctx context.Context, spec Spec) error
}

// Spec is what the person asked for and what the host has prepared, in terms
// that do not depend on how the VM is run.
type Spec struct {
	// Image is the folder with the image of the VM. Which files the backend
	// needs in it is its own business.
	Image string
	// State is where the backend keeps the disk of the project that survives
	// restarts, and StateBytes the size it makes a new one with. An
	// existing disk keeps its size.
	State      string
	StateBytes int64
	MemoryMiB  int
	CPUs       int
	// Project and Home are the host folders that appear in the VM as
	// /project and /home/user, writable.
	Project string
	Home    string
	// Mounts are further host folders, which appear read-only in the VM.
	Mounts []Mount
	// Shell opens a shell in the VM instead of Claude Code.
	Shell bool
	// Task is the host folder of a task the VM runs unattended, shared
	// read-only. The VM then has neither Project nor Home and no terminal:
	// Stdout gets the results and Progress what the task is doing. It has
	// the Mounts, which are read-only too.
	Task     string
	Progress io.Writer
	// RemoveState removes the state disk as soon as the VM has it open, so
	// that it is gone with the VM. aibox can remove no file once the VM
	// runs.
	RemoveState bool
	// Unsandboxed runs the VM without the protection the host puts around
	// it, to debug it.
	Unsandboxed bool
	// Env are variables for the command in the VM, as NAME=value.
	Env []string
	// Proxy decides which connections the VM may make through the host.
	// Ports are the TCP ports of the hosts it allows, for a backend that
	// shuts aibox itself off from every other port.
	Proxy proxy.Options
	Ports []uint16
	// Loopback are the ports on the loopback of the host that the proxy
	// lets the VM reach. The VM has them on its own loopback.
	Loopback []uint16
	// ConsoleLog is the file the console of the VM is written to. Empty
	// throws it away.
	ConsoleLog string
	// Stdin and Stdout are the terminal of the person, Stderr is where the
	// messages of aibox go. A task has no Stdin.
	Stdin  *os.File
	Stdout io.Writer
	Stderr io.Writer
}

// Mount is a host folder that appears read-only at Guest in the VM.
type Mount struct {
	Host  string
	Guest string
}

// ExitError is a command in the VM that ended with a code other than 0,
// which aibox ends with too. It has no ExitCode method on purpose:
// urfave/cli would then exit by itself.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("the command in the VM ended with exit code %d", e.Code)
}
