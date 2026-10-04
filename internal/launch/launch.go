// Package launch runs the aibox VM: virtiofsd for each share, the proxy and
// the terminal for the VM, then QEMU.
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/session"
	"github.com/the127/aibox/internal/vm"
)

// ErrSocketTimeout is returned when virtiofsd does not create its socket in
// time.
var ErrSocketTimeout = errors.New("virtiofsd did not create its socket in time")

const (
	defaultSocketTimeout = 10 * time.Second
	defaultKVMDevice     = "/dev/kvm"
	defaultVhostDevice   = "/dev/vhost-vsock"
	stopDelay            = time.Second
	// sessionEndDelay is how long the session may go on after QEMU has
	// exited, to show the last output of the VM.
	sessionEndDelay = 3 * time.Second
)

// Options are the programs Run starts, the terminal of the person on Stdin
// and Stdout, where the messages of aibox and of the programs go, and how
// the VM reaches the host.
type Options struct {
	QEMU      string
	Virtiofsd string
	Stdin     *os.File
	Stdout    io.Writer
	Stderr    io.Writer
	// SocketTimeout is how long virtiofsd may take to create its socket.
	// Zero means ten seconds.
	SocketTimeout time.Duration
	// ListenVsock opens a listener the VM reaches the host on and returns
	// its port. Nil listens on vsock.
	ListenVsock func() (net.Listener, uint32, error)
	Proxy       proxy.Options
	// Env are variables for the command in the VM, as NAME=value.
	Env []string
	// KVMDevice and VhostDevice are the device files QEMU gets. Empty means
	// /dev/kvm and /dev/vhost-vsock.
	KVMDevice   string
	VhostDevice string
	// ConsoleLog is the file the console of the VM is written to. Empty
	// throws it away.
	ConsoleLog string
}

// Run boots the machine with the proxy and the terminal listening for it
// and returns when QEMU exits, or with the error of the context when it
// ends first. On return the proxy and the virtiofsd processes are stopped,
// their sockets are removed and the terminal is as it was.
func Run(ctx context.Context, machine vm.Machine, options Options) error {
	if options.SocketTimeout == 0 {
		options.SocketTimeout = defaultSocketTimeout
	}

	if options.Stderr == nil {
		options.Stderr = io.Discard
	}

	if options.ListenVsock == nil {
		options.ListenVsock = listenVsock
	}

	if options.KVMDevice == "" {
		options.KVMDevice = defaultKVMDevice
	}

	if options.VhostDevice == "" {
		options.VhostDevice = defaultVhostDevice
	}

	sockets, err := os.MkdirTemp("", "aibox-")
	if err != nil {
		return fmt.Errorf("create the socket folder: %w", err)
	}

	defer func() { _ = os.RemoveAll(sockets) }()

	machine.Shares = withSockets(machine.Shares, sockets)

	died, stopDaemons, err := startDaemons(machine.Shares, machine.Owner, options)
	defer stopDaemons()

	if err != nil {
		return err
	}

	if err := waitForSockets(ctx, machine.Shares, options.SocketTimeout, died); err != nil {
		return err
	}

	files, err := openFiles(machine, options)
	if err != nil {
		return err
	}

	defer files.close()

	logged := logConsole(files.console, options.ConsoleLog, options.Stderr)

	port, stopProxy, err := serveProxy(ctx, machine.GuestCID, options)
	if err != nil {
		return err
	}

	defer stopProxy()

	machine.ProxyPort = port

	terminalPort, stopTerminal, err := serveTerminal(ctx, machine.GuestCID, options)
	if err != nil {
		return err
	}

	machine.TerminalPort = terminalPort

	err = runQEMU(ctx, machine, files, options)

	<-logged

	if attached := stopTerminal(); !attached && err == nil && options.ConsoleLog != "" {
		_, _ = fmt.Fprintf(options.Stderr, "aibox: the VM ended before its terminal came up, see %s\n", options.ConsoleLog)
	}

	return err
}

// qemuFiles are the files QEMU inherits beyond its standard three, in the
// order of their numbers in QEMU, and aibox's end of the console pair.
type qemuFiles struct {
	extra   []*os.File
	numbers vm.Files
	console *os.File
}

// openFiles opens everything QEMU needs, so that QEMU opens no path itself
// and a missing file is reported before it starts. The caller closes the
// extra files once QEMU has them, and the console when the log is done.
func openFiles(machine vm.Machine, options Options) (_ *qemuFiles, err error) {
	files := &qemuFiles{}

	defer func() {
		if err != nil {
			files.close()

			if files.console != nil {
				_ = files.console.Close()
			}
		}
	}()

	if files.numbers.KVM, err = files.open("KVM device", options.KVMDevice, os.O_RDWR); err != nil {
		return nil, err
	}

	if files.numbers.Vhost, err = files.open("vhost-vsock device", options.VhostDevice, os.O_RDWR); err != nil {
		return nil, err
	}

	if files.numbers.Kernel, err = files.open("kernel", machine.Kernel, os.O_RDONLY); err != nil {
		return nil, err
	}

	if files.numbers.Rootfs, err = files.open("root disk", machine.Rootfs, os.O_RDONLY); err != nil {
		return nil, err
	}

	for _, share := range machine.Shares {
		socket, err := connect(share)
		if err != nil {
			return nil, err
		}

		files.numbers.Shares = append(files.numbers.Shares, files.add(socket))
	}

	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("create the console socket: %w", err)
	}

	files.numbers.Console = files.add(os.NewFile(uintptr(pair[0]), "console"))
	files.console = os.NewFile(uintptr(pair[1]), "console")

	return files, nil
}

// open opens the path and returns the number the file has in QEMU.
func (f *qemuFiles) open(what, path string, flag int) (int, error) {
	file, err := os.OpenFile(path, flag, 0) //nolint:gosec // the paths come from the config and the image
	if err != nil {
		return 0, fmt.Errorf("open the %s: %w", what, err)
	}

	return f.add(file), nil
}

// add takes the file and returns the number it will have in QEMU, which
// inherits the extra files after its own three.
func (f *qemuFiles) add(file *os.File) int {
	f.extra = append(f.extra, file)

	return 2 + len(f.extra)
}

// close closes aibox's copies of the extra files. It may be called again.
func (f *qemuFiles) close() {
	for _, file := range f.extra {
		_ = file.Close()
	}

	f.extra = nil
}

// connect connects to the virtiofsd of the share and returns the connection
// as a file for QEMU.
func connect(share vm.Share) (*os.File, error) {
	conn, err := net.Dial("unix", share.Socket)
	if err != nil {
		return nil, fmt.Errorf("connect to virtiofsd for %s: %w", share.Dir, err)
	}

	defer func() { _ = conn.Close() }()

	file, err := conn.(*net.UnixConn).File()
	if err != nil {
		return nil, fmt.Errorf("connect to virtiofsd for %s: %w", share.Dir, err)
	}

	return file, nil
}

// logConsole copies the console of the VM into the log file until the VM
// is gone, and closes the returned channel then. Without a path, or when
// the file cannot be opened, the console is thrown away.
func logConsole(console *os.File, path string, stderr io.Writer) <-chan struct{} {
	log := io.Discard

	if path != "" {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the path is the project's console log
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "aibox: open the console log: %v\n", err)
		} else {
			log = file
		}
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		_, _ = io.Copy(log, console)
		_ = console.Close()

		if file, ok := log.(*os.File); ok {
			_ = file.Close()
		}
	}()

	return done
}

// startDaemons starts virtiofsd for each share. The channel gets the exit
// of each daemon, and the returned function stops the daemons and waits
// for them.
func startDaemons(shares []vm.Share, owner *vm.Owner, options Options) (<-chan error, func(), error) {
	// the daemons stop after QEMU, not with it, so a cancelled context ends
	// QEMU first
	ctx, cancel := context.WithCancel(context.Background())

	var running sync.WaitGroup

	stop := func() {
		cancel()
		running.Wait()
	}

	died := make(chan error, len(shares))

	for _, share := range shares {
		daemon := command(ctx, options.Virtiofsd, share.VirtiofsdArgs(owner))
		daemon.Stderr = options.Stderr
		// a Ctrl-C from the terminal reaches QEMU alone
		daemon.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		if err := daemon.Start(); err != nil {
			return died, stop, fmt.Errorf("start virtiofsd for %s: %w", share.Dir, err)
		}

		running.Add(1)

		go func() {
			defer running.Done()

			died <- fmt.Errorf("virtiofsd for %s exited: %w", share.Dir, daemon.Wait())
		}()
	}

	return died, stop, nil
}

// serveProxy listens for the VM and serves the proxy to it until the
// returned function is called, which also waits for the proxy to stop.
func serveProxy(ctx context.Context, cid uint32, options Options) (uint32, func(), error) {
	listener, port, err := options.ListenVsock()
	if err != nil {
		return 0, nil, fmt.Errorf("listen for the VM: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)

	go func() { done <- proxy.Serve(ctx, forGuest(listener, cid), options.Proxy) }()

	stop := func() {
		cancel()

		if err := <-done; err != nil {
			_, _ = fmt.Fprintf(options.Stderr, "aibox: the proxy stopped: %v\n", err)
		}
	}

	return port, stop, nil
}

// serveTerminal listens for the VM and runs its session on the terminal of
// the person until the returned function is called. That function lets the
// session end, waits for the terminal to be restored and reports whether
// the VM ever connected.
func serveTerminal(ctx context.Context, cid uint32, options Options) (uint32, func() bool, error) {
	listener, port, err := options.ListenVsock()
	if err != nil {
		return 0, nil, fmt.Errorf("listen for the terminal of the VM: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan bool, 1)

	go func() { done <- attach(ctx, forGuest(listener, cid), options) }()

	stop := func() bool {
		_ = listener.Close()

		select {
		case attached := <-done:
			return attached
		case <-time.After(sessionEndDelay):
			cancel()

			return <-done
		}
	}

	return port, stop, nil
}

// attach waits for the VM to connect and runs the session on the terminal
// of the person. It reports whether the VM connected. The connection is
// closed when the context ends.
func attach(ctx context.Context, listener net.Listener, options Options) bool {
	conn, err := listener.Accept()
	if err != nil {
		return false
	}

	defer func() { _ = conn.Close() }()

	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	client, restore, err := session.NewClient(options.Stdin, options.Stdout)
	if err != nil {
		_, _ = fmt.Fprintf(options.Stderr, "aibox: prepare the terminal: %v\n", err)

		return true
	}

	client.Env = options.Env

	code, err := client.Attach(conn)

	restore()

	switch {
	case err != nil && ctx.Err() == nil:
		_, _ = fmt.Fprintf(options.Stderr, "aibox: %v\n", err)
	case err == nil && code != 0:
		_, _ = fmt.Fprintf(options.Stderr, "aibox: the command in the VM ended with exit code %d\n", code)
	}

	return true
}

// runQEMU runs QEMU with its output on Stderr, because Stdout belongs to
// the session.
func runQEMU(ctx context.Context, machine vm.Machine, files *qemuFiles, options Options) error {
	qemu := command(ctx, options.QEMU, machine.QEMUArgs(files.numbers))
	qemu.Stdout = options.Stderr
	qemu.Stderr = options.Stderr
	qemu.ExtraFiles = files.extra

	err := qemu.Start()

	// QEMU has its own copies now, or never will, and the console log ends
	// only when every copy of its end is closed
	files.close()

	if err != nil {
		return fmt.Errorf("run qemu: %w", err)
	}

	if err := qemu.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return fmt.Errorf("run qemu: %w", err)
	}

	return nil
}

// command stops the program with SIGTERM when the context ends and kills it
// when it has not exited after stopDelay.
func command(ctx context.Context, program string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, program, args...) //nolint:gosec // the caller chooses the program
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = stopDelay

	return cmd
}

func withSockets(shares []vm.Share, dir string) []vm.Share {
	result := make([]vm.Share, len(shares))

	for i, share := range shares {
		share.Socket = filepath.Join(dir, share.Tag+".sock")
		result[i] = share
	}

	return result
}

func waitForSockets(ctx context.Context, shares []vm.Share, timeout time.Duration, died <-chan error) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)

	defer ticker.Stop()

	for _, share := range shares {
		for !isSocket(share.Socket) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-died:
				return err
			case <-deadline:
				return fmt.Errorf("share %s: %w", share.Dir, ErrSocketTimeout)
			case <-ticker.C:
			}
		}
	}

	return nil
}

func isSocket(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode()&os.ModeSocket != 0
}

func listenVsock() (net.Listener, uint32, error) {
	listener, err := vsock.Listen(0, nil)
	if err != nil {
		return nil, 0, err
	}

	return listener, listener.Addr().(*vsock.Addr).Port, nil
}

// forGuest returns a listener that accepts the connections of the VM with
// the context ID and closes all others. A vsock listener on the host gets
// the connections of every VM.
func forGuest(listener net.Listener, cid uint32) net.Listener {
	return &guestListener{Listener: listener, cid: cid}
}

type guestListener struct {
	net.Listener
	cid uint32
}

func (l *guestListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}

		if addr, ok := conn.RemoteAddr().(*vsock.Addr); ok && addr.ContextID == l.cid {
			return conn, nil
		}

		_ = conn.Close()
	}
}
