// Package apple builds the command line of Apple's container tool for the
// aibox VM. Each container there is a VM of its own.
package apple

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrComma is a host folder whose path has a comma, which would end the
// path early in the list of options of a mount.
var ErrComma = errors.New("a path with a comma cannot be mounted")

// ErrColon is a socket whose path has a colon, which separates the host
// path from the guest path.
var ErrColon = errors.New("a socket path with a colon cannot be published")

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
	args := []string{
		"run", "--rm", "--name", m.Name,
		// the VM reaches the network only through the proxy of the host
		"--network", "none",
		"--cpus", strconv.Itoa(m.CPUs),
		"--memory", strconv.Itoa(m.MemoryMiB) + "M",
	}

	binds := []Mount{{Host: m.Project, Guest: "/project"}, {Host: m.Home, Guest: "/home/user"}}
	for _, bind := range binds {
		option, err := mountOption(bind, false)
		if err != nil {
			return nil, err
		}

		args = append(args, "--mount", option)
	}

	for _, mount := range m.Mounts {
		option, err := mountOption(mount, true)
		if err != nil {
			return nil, err
		}

		args = append(args, "--mount", option)
	}

	if strings.Contains(m.TerminalSocket, ":") {
		return nil, fmt.Errorf("%s: %w", m.TerminalSocket, ErrColon)
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

	return append(args, m.Image), nil
}

func mountOption(mount Mount, readOnly bool) (string, error) {
	for _, path := range []string{mount.Host, mount.Guest} {
		if strings.Contains(path, ",") {
			return "", fmt.Errorf("%s: %w", path, ErrComma)
		}
	}

	option := "type=bind,source=" + mount.Host + ",target=" + mount.Guest
	if readOnly {
		option += ",readonly"
	}

	return option, nil
}
