// Package vsockns gives a VM a vsock namespace of its own: a network
// namespace in local mode, where the guest CID is private, so every VM can
// have the same one, and where only aibox has sockets.
package vsockns

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

// Command is the hidden command of aibox that runs the helper.
const Command = "vsock-namespace"

const (
	childMode   = "/proc/sys/net/vsock/child_ns_mode"
	mode        = "/proc/sys/net/vsock/ns_mode"
	vhostDevice = "/dev/vhost-vsock"
	self        = "/proc/self/exe"
	// the socket the helper reports on, after its standard three files
	reportFD      = 3
	backlog       = 8
	helperTimeout = 10 * time.Second
	// what the helper sends: the vhost device, then the proxy and the
	// terminal listener, and the two ports
	sentFiles = 3
	sentPorts = 2
)

// ErrNoNamespaces is a kernel without vsock network namespaces.
var ErrNoNamespaces = errors.New("the kernel has no vsock network namespaces")

// Vsock is what a VM and aibox talk over: the vhost-vsock device, opened in
// the namespace, and the listeners the VM reaches aibox on, with their
// ports. The caller owns all three.
type Vsock struct {
	Vhost        *os.File
	Proxy        net.Listener
	Terminal     net.Listener
	ProxyPort    uint32
	TerminalPort uint32
}

// Open starts aibox again with the command as a helper in a new user and
// network namespace, which makes the vsock namespace and sends the device
// and the listeners back. They keep the namespace alive after the helper
// has exited.
func Open(command ...string) (*Vsock, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("create the socket for the namespace helper: %w", err)
	}

	theirs, ours := os.NewFile(uintptr(pair[0]), "helper"), os.NewFile(uintptr(pair[1]), "helper")
	defer func() { _ = ours.Close() }()

	var stderr bytes.Buffer

	helper := exec.Command(self, command...) //nolint:gosec // aibox starts itself with a fixed command
	helper.Stderr = &stderr
	helper.ExtraFiles = []*os.File{theirs}
	helper.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}

	err = helper.Start()

	_ = theirs.Close()

	if err != nil {
		return nil, fmt.Errorf("start the namespace helper, which needs unprivileged user namespaces: %w", err)
	}

	// a helper stuck before it reports would hold aibox forever
	killer := time.AfterFunc(helperTimeout, func() { _ = helper.Process.Kill() })
	defer killer.Stop()

	files, ports, err := receive(ours)

	if waitErr := helper.Wait(); waitErr != nil {
		closeAll(files)

		if reason := strings.TrimSpace(stderr.String()); reason != "" {
			return nil, fmt.Errorf("the namespace helper failed: %s", reason)
		}

		return nil, fmt.Errorf("the namespace helper failed: %w", errors.Join(waitErr, err))
	}

	if err != nil {
		closeAll(files)

		return nil, fmt.Errorf("receive the vsock from the namespace helper: %w", err)
	}

	return fromFiles(files, ports)
}

// fromFiles takes over the files the helper sent. The listeners are copies,
// so the files behind them are closed here.
func fromFiles(files []*os.File, ports [sentPorts]uint32) (*Vsock, error) {
	vhost, proxyFile, terminalFile := files[0], files[1], files[2]

	proxy, err := vsock.FileListener(proxyFile)
	if err != nil {
		closeAll(files)

		return nil, fmt.Errorf("take over the proxy listener: %w", err)
	}

	terminal, err := vsock.FileListener(terminalFile)
	if err != nil {
		_ = proxy.Close()
		closeAll(files)

		return nil, fmt.Errorf("take over the terminal listener: %w", err)
	}

	_ = proxyFile.Close()
	_ = terminalFile.Close()

	return &Vsock{Vhost: vhost, Proxy: proxy, Terminal: terminal, ProxyPort: ports[0], TerminalPort: ports[1]}, nil
}

func closeAll(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

// Serve is the helper. It runs in a new user and network namespace, makes
// the vsock namespace inside it, and reports the device and the listeners
// on its reporting socket.
func Serve() error {
	// the namespace is a property of this thread
	runtime.LockOSThread()

	if err := os.WriteFile(childMode, []byte("local"), 0); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s is missing", ErrNoNamespaces, childMode)
		}

		return fmt.Errorf("make the vsock namespace local: %w", err)
	}

	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		return fmt.Errorf("enter the vsock namespace: %w", err)
	}

	// a global namespace would share its CIDs with the whole host
	if got, err := os.ReadFile(mode); err != nil || strings.TrimSpace(string(got)) != "local" {
		return fmt.Errorf("the vsock namespace is not local: %q, %w", strings.TrimSpace(string(got)), err)
	}

	vhost, err := os.OpenFile(vhostDevice, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open the vhost-vsock device: %w", err)
	}

	proxy, proxyPort, err := listen()
	if err != nil {
		return fmt.Errorf("listen for the proxy: %w", err)
	}

	terminal, terminalPort, err := listen()
	if err != nil {
		return fmt.Errorf("listen for the terminal: %w", err)
	}

	return send(os.NewFile(reportFD, "report"), []*os.File{vhost, proxy, terminal}, [sentPorts]uint32{proxyPort, terminalPort})
}

// listen opens a vsock listener on a port the kernel picks.
func listen() (*os.File, uint32, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, 0, err
	}

	port, err := bindAndListen(fd)
	if err != nil {
		_ = unix.Close(fd)

		return nil, 0, err
	}

	return os.NewFile(uintptr(fd), "listener"), port, nil
}

func bindAndListen(fd int) (uint32, error) {
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: unix.VMADDR_PORT_ANY}); err != nil {
		return 0, err
	}

	if err := unix.Listen(fd, backlog); err != nil {
		return 0, err
	}

	name, err := unix.Getsockname(fd)
	if err != nil {
		return 0, err
	}

	addr, ok := name.(*unix.SockaddrVM)
	if !ok {
		return 0, errors.New("the socket has no vsock address")
	}

	return addr.Port, nil
}

// send sends the files and the ports over the socket in one message.
func send(socket *os.File, files []*os.File, ports [sentPorts]uint32) error {
	fds := make([]int, len(files))
	for i, file := range files {
		fds[i] = int(file.Fd())
	}

	data := make([]byte, 4*sentPorts)
	for i, port := range ports {
		binary.LittleEndian.PutUint32(data[4*i:], port)
	}

	err := unix.Sendmsg(int(socket.Fd()), data, unix.UnixRights(fds...), nil, 0)

	// the files must not be collected before the kernel has copied them
	runtime.KeepAlive(files)
	runtime.KeepAlive(socket)

	return err
}

// receive receives what send sent. The files are not inherited by children.
func receive(socket *os.File) ([]*os.File, [sentPorts]uint32, error) {
	var ports [sentPorts]uint32

	data := make([]byte, 4*sentPorts)
	oob := make([]byte, unix.CmsgSpace(sentFiles*4))

	n, oobn, flags, _, err := unix.Recvmsg(int(socket.Fd()), data, oob, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return nil, ports, err
	}

	files, err := receivedFiles(oob[:oobn])
	if err != nil {
		return nil, ports, err
	}

	if flags&unix.MSG_CTRUNC != 0 {
		closeAll(files)

		return nil, ports, errors.New("the message with the files was cut off")
	}

	if n != len(data) || len(files) != sentFiles {
		closeAll(files)

		return nil, ports, fmt.Errorf("got %d bytes and %d files, not %d and %d", n, len(files), len(data), sentFiles)
	}

	for i := range ports {
		ports[i] = binary.LittleEndian.Uint32(data[4*i:])
	}

	return files, ports, nil
}

func receivedFiles(oob []byte) ([]*os.File, error) {
	messages, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}

	var files []*os.File

	for _, message := range messages {
		fds, err := unix.ParseUnixRights(&message)
		if err != nil {
			closeAll(files)

			return nil, err
		}

		for _, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), "received"))
		}
	}

	return files, nil
}
