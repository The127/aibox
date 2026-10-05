// Package apple builds the command line of Apple's container tool for the
// aibox VM. Each container there is a VM of its own.
package apple

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrMountPath is a folder path that is empty or that holds a comma or an
// equals sign, which the tool would read as the end of the path in the
// options of a mount.
var ErrMountPath = errors.New("a mount needs a path without a comma or an equals sign")

// ErrSocketPath is a socket path that is empty or that holds a colon, which
// separates the host path from the guest path.
var ErrSocketPath = errors.New("a socket needs a path without a colon")

// ErrName is a name of the container, the image or the volume that is empty
// or that the tool would take for a flag, or a name of a volume with a colon,
// which ends it.
var ErrName = errors.New("a name must not be empty or start with a dash, and a volume has no colon")

// ErrSize is a VM without a CPU or without memory.
var ErrSize = errors.New("a VM needs at least one CPU and one MiB of memory")

// GuestTerminalSocket is where the terminal of the VM listens in the guest.
// The host reaches it through Machine.TerminalSocket.
const GuestTerminalSocket = "/run/aibox/terminal.sock"

// guestState is where the state volume is mounted in the guest.
const guestState = "/var/lib/aibox/state"

// Machine is a container with the image, sized as the person asked. Project
// and Home are the host folders that appear writable as /project and
// /home/user. State is the name of the volume of the project. The host
// connects to the terminal of the VM over the socket at TerminalSocket.
type Machine struct {
	Name           string
	Image          string
	State          string
	MemoryMiB      int
	CPUs           int
	Project        string
	Home           string
	Mounts         []Mount
	Shell          bool
	TerminalSocket string
}

// Mount is a host folder that appears read-only at Guest.
type Mount struct {
	Host  string
	Guest string
}

// RunArgs returns the arguments of `container run` for the machine.
func (m Machine) RunArgs() ([]string, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}

	args := []string{
		"run", "--rm", "--name", m.Name,
		// the VM reaches the network only through the proxy of the host
		"--network", "none",
		"--cpus", strconv.Itoa(m.CPUs),
		"--memory", strconv.Itoa(m.MemoryMiB) + "M",
		"--mount", mountOption(Mount{Host: m.Project, Guest: "/project"}, false),
		"--mount", mountOption(Mount{Host: m.Home, Guest: "/home/user"}, false),
	}

	for _, mount := range m.Mounts {
		args = append(args, "--mount", mountOption(mount, true))
	}

	args = append(args,
		"--volume", m.State+":"+guestState,
		"--publish-socket", m.TerminalSocket+":"+GuestTerminalSocket,
	)

	// the guest reads its settings from the kernel command line, as it does
	// under QEMU
	if m.Shell {
		args = append(args, "--kernel-arg", "aibox.shell")
	}

	return append(args, "--", m.Image), nil
}

func (m Machine) validate() error {
	for _, name := range []string{m.Name, m.Image, m.State} {
		if name == "" || strings.HasPrefix(name, "-") {
			return fmt.Errorf("%q: %w", name, ErrName)
		}
	}

	// an image has a colon before its tag, a volume must not
	if strings.Contains(m.State, ":") {
		return fmt.Errorf("%q: %w", m.State, ErrName)
	}

	if m.CPUs < 1 || m.MemoryMiB < 1 {
		return fmt.Errorf("%d CPUs and %d MiB: %w", m.CPUs, m.MemoryMiB, ErrSize)
	}

	paths := []string{m.Project, m.Home}
	for _, mount := range m.Mounts {
		paths = append(paths, mount.Host, mount.Guest)
	}

	for _, path := range paths {
		if path == "" || strings.ContainsAny(path, ",=") {
			return fmt.Errorf("%q: %w", path, ErrMountPath)
		}
	}

	if m.TerminalSocket == "" || strings.Contains(m.TerminalSocket, ":") {
		return fmt.Errorf("%q: %w", m.TerminalSocket, ErrSocketPath)
	}

	return nil
}

func mountOption(mount Mount, readOnly bool) string {
	option := "type=bind,source=" + mount.Host + ",target=" + mount.Guest
	if readOnly {
		option += ",readonly"
	}

	return option
}
