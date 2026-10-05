//go:build linux

package guest

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// Platform is what differs between the ways a VM is run: how the root, the
// console, the shares and the state disk come to be, and how the guest
// reaches the host. Run calls each method once, at its place in the setup.
type Platform interface {
	// Prepare readies the root for the mount points, before anything is
	// mounted.
	Prepare(sys System) error
	// Premounted are the targets of the kernel file systems the platform
	// mounted already, which the init leaves as they are.
	Premounted() []string
	// Console opens where the messages of the init go.
	Console(sys System, options Options) (*os.File, error)
	// Shares mounts the project and the home.
	Shares(sys System) error
	// Cgroups readies the cgroup tree for handing controllers down.
	Cgroups(sys System) error
	// State mounts the state disk of the project on stateMount.
	State(sys System) error
	// Folders mounts the further folders of the host, read-only.
	Folders(sys System, options Options) error
	// Connect returns how the guest reaches the host, once the root is
	// locked.
	Connect(sys System, options Options) (Transport, error)
}

// ErrNoProxyPort is a proxy the kernel command line names without the vsock
// port it is reached on.
var ErrNoProxyPort = errors.New("aibox.proxy names no vsock port")

// ErrUnknownPlatform is an argument of the init that names no platform.
var ErrUnknownPlatform = errors.New("no such platform")

// PlatformOf returns the platform the arguments of the init name. The kernel
// under QEMU starts the init without arguments, a runtime that starts it as
// the process of a container passes "container".
func PlatformOf(args []string) (Platform, error) {
	if len(args) == 0 {
		return QEMU{}, nil
	}

	if args[0] == "container" {
		return &Container{}, nil
	}

	return nil, fmt.Errorf("%q: %w", args[0], ErrUnknownPlatform)
}

// Transport is how the guest reaches the terminal and the proxy of the host.
type Transport interface {
	DialTerminal() (net.Conn, error)
	DialProxy() (net.Conn, error)
}

// QEMU is the platform of a VM that QEMU boots from a read-only root disk,
// with the shares on virtiofs, the state on a second disk and the host
// behind vsock.
type QEMU struct{}

// Prepare puts an overlay in RAM over the read-only root disk and makes it
// the root, so that the mount points of the shares can be made.
func (QEMU) Prepare(sys System) error {
	if err := sys.Mount("tmpfs", overlayDir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=755,size=16m"); err != nil {
		return fmt.Errorf("mount the overlay tmpfs: %w", err)
	}

	for _, dir := range []string{overlayDir + "/upper", overlayDir + "/work"} {
		if err := sys.Mkdir(dir); err != nil {
			return fmt.Errorf("make %s: %w", dir, err)
		}
	}

	if err := sys.Mount("overlay", overlayRoot, "overlay", 0, overlayData); err != nil {
		return fmt.Errorf("mount the overlay: %w", err)
	}

	if err := sys.PivotRoot(overlayRoot, oldRoot); err != nil {
		return fmt.Errorf("make %s the root: %w", overlayRoot, err)
	}

	return nil
}

// Premounted is empty, the kernel leaves every mount to the init.
func (QEMU) Premounted() []string { return nil }

// Console opens the console the kernel command line names.
func (QEMU) Console(sys System, options Options) (*os.File, error) {
	console, err := sys.OpenConsole(options.Console)
	if err != nil {
		return nil, fmt.Errorf("open the console %s: %w", options.Console, err)
	}

	return console, nil
}

// Shares mounts the project and the home from virtiofs.
func (QEMU) Shares(sys System) error {
	for _, share := range []struct{ tag, target string }{{projectShare, project}, {homeShare, home}} {
		if err := sys.Mount(share.tag, share.target, "virtiofs", 0, ""); err != nil {
			return fmt.Errorf("mount %s on %s: %w", share.tag, share.target, err)
		}
	}

	return nil
}

// Cgroups has nothing to do: the init is in the root cgroup, which may hand
// controllers down with processes in it.
func (QEMU) Cgroups(System) error { return nil }

// State mounts the second disk, formatting it on the first boot.
func (QEMU) State(sys System) error {
	blank, err := sys.Blank(stateDevice)
	if err != nil {
		return fmt.Errorf("look at the state disk, state.ext4 of the project on the host: %w", err)
	}

	if blank {
		if err := sys.Format(stateDevice); err != nil {
			return fmt.Errorf("format the state disk: %w", err)
		}
	}

	if err := sys.Mount(stateDevice, stateMount, "ext4", stateFlags, ""); err != nil {
		return fmt.Errorf("mount the state disk: %w", err)
	}

	return nil
}

// Folders mounts the shares the kernel command line names.
func (QEMU) Folders(sys System, options Options) error {
	for _, m := range options.Mounts {
		if err := sys.Mount(m.Tag, m.Path, "virtiofs", readOnlyShare, ""); err != nil {
			return fmt.Errorf("mount %s on %s: %w", m.Tag, m.Path, err)
		}
	}

	return nil
}

// Connect reaches the host over the vsock ports of the kernel command line.
func (QEMU) Connect(sys System, options Options) (Transport, error) {
	return vsockTransport{network: sys, options: options}, nil
}

type vsockTransport struct {
	network Network
	options Options
}

func (t vsockTransport) DialTerminal() (net.Conn, error) {
	if t.options.TerminalPort == 0 {
		return nil, ErrNoTerminal
	}

	conn, err := t.network.DialHost(t.options.TerminalPort)
	if err != nil {
		return nil, fmt.Errorf("connect to the terminal on the host: %w", err)
	}

	return conn, nil
}

func (t vsockTransport) DialProxy() (net.Conn, error) {
	if t.options.ProxyPort == 0 {
		return nil, ErrNoProxyPort
	}

	return t.network.DialHost(t.options.ProxyPort)
}
