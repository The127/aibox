//go:build linux

package guest

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/the127/aibox/internal/link"
)

const (
	// linkSocket is where the guest listens for the host. It is on the root
	// disk, since the runtime does not reach a socket on a file system the
	// init mounts itself, and it is made while the root is still writable.
	linkSocket = "/var/lib/aibox/link.sock"
	// containerConsole is the output of the first process, which the
	// runtime hands to the host. The console of the kernel belongs to the
	// runtime.
	containerConsole = "/proc/self/fd/1"
	// initCgroup is where the init goes, because a cgroup with processes in
	// it cannot hand controllers down, and under a runtime the init does
	// not start in the root cgroup, which may.
	initCgroup = cgroupRoot + "/init"
)

// Container is the platform of a VM whose runtime mounts the project, the
// home, the further folders and the state disk itself, on a writable root,
// and lets the host connect to a socket in the guest.
type Container struct {
	listener net.Listener
}

// Prepare listens for the host while the root can still take the socket.
func (c *Container) Prepare(sys System) error {
	if err := sys.Mkdir(filepath.Dir(linkSocket)); err != nil {
		return fmt.Errorf("make %s: %w", filepath.Dir(linkSocket), err)
	}

	listener, err := sys.ListenSocket(linkSocket)
	if err != nil {
		return fmt.Errorf("listen for the host on %s: %w", linkSocket, err)
	}

	c.listener = listener

	return nil
}

// Premounted are the file systems the runtime mounted with the options the
// init would give them. The kernel refuses to mount them a second time.
func (*Container) Premounted() []string {
	return []string{"/dev", "/sys", "/sys/fs/cgroup"}
}

// Console opens the output the runtime hands to the host.
func (*Container) Console(sys System, _ Options) (*os.File, error) {
	console, err := sys.OpenConsole(containerConsole)
	if err != nil {
		return nil, fmt.Errorf("open the console %s: %w", containerConsole, err)
	}

	return console, nil
}

// Shares has nothing to do, the runtime mounted them.
func (*Container) Shares(System) error { return nil }

// Cgroups moves the init out of the cgroup it started in.
func (*Container) Cgroups(sys System) error {
	if err := sys.Enter(initCgroup); err != nil {
		return fmt.Errorf("move the init to %s: %w", initCgroup, err)
	}

	return nil
}

// State has nothing to do, the runtime mounted the volume.
func (*Container) State(System) error { return nil }

// Folders has nothing to do, the runtime mounted them read-only.
func (*Container) Folders(System, Options) error { return nil }

// Connect waits for the host on the socket in the background and carries
// the terminal and the proxy over the link it makes.
func (c *Container) Connect(System, Options) (Transport, error) {
	t := &linkTransport{ready: make(chan struct{})}

	go t.accept(c.listener)

	return t, nil
}

type linkTransport struct {
	ready chan struct{}
	end   *link.GuestEnd
	err   error
}

// accept takes the one connection of the host.
func (t *linkTransport) accept(listener net.Listener) {
	defer close(t.ready)

	conn, err := listener.Accept()
	_ = listener.Close()

	if err != nil {
		t.err = fmt.Errorf("wait for the host: %w", err)

		return
	}

	t.end, t.err = link.Guest(conn)
}

func (t *linkTransport) DialTerminal() (net.Conn, error) {
	<-t.ready

	if t.err != nil {
		return nil, t.err
	}

	conn, err := t.end.DialTerminal()
	if err != nil {
		return nil, fmt.Errorf("connect to the terminal on the host: %w", err)
	}

	return conn, nil
}

func (t *linkTransport) DialProxy() (net.Conn, error) {
	<-t.ready

	if t.err != nil {
		return nil, t.err
	}

	return t.end.DialProxy()
}
