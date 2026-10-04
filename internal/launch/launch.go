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
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"github.com/the127/aibox/internal/confine"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/sandbox"
	"github.com/the127/aibox/internal/session"
	"github.com/the127/aibox/internal/vm"
	"github.com/the127/aibox/internal/vsockns"
)

// ErrSocketTimeout is returned when virtiofsd does not create its socket in
// time.
var ErrSocketTimeout = errors.New("virtiofsd did not create its socket in time")

const (
	defaultSocketTimeout = 10 * time.Second
	defaultKVMDevice     = "/dev/kvm"
	defaultBubblewrap    = "bwrap"
	// what QEMU is made of on the host, bound into its sandbox
	libraries = "/usr/lib64"
	firmware  = "/usr/share/qemu/qboot.rom"
	stopDelay = time.Second
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
	// OpenVsock opens the vsock the VM and aibox talk over. Nil runs the
	// namespace helper of aibox.
	OpenVsock func() (*vsockns.Vsock, error)
	Proxy     proxy.Options
	// Env are variables for the command in the VM, as NAME=value.
	Env []string
	// KVMDevice is the KVM device file QEMU gets. Empty means /dev/kvm.
	KVMDevice string
	// Bubblewrap is the program that runs QEMU in its sandbox. Empty means
	// bwrap on the PATH.
	Bubblewrap string
	// NoSandbox runs QEMU without the sandbox.
	NoSandbox bool
	// Confine is called once QEMU has started, with the TCP ports the proxy
	// may still connect to. Nil means confine.Apply.
	Confine func(ports []uint16) error
	// Ports are the TCP ports of the allow list.
	Ports []uint16
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

	if options.OpenVsock == nil {
		options.OpenVsock = func() (*vsockns.Vsock, error) { return vsockns.Open(vsockns.Command) }
	}

	if options.KVMDevice == "" {
		options.KVMDevice = defaultKVMDevice
	}

	if options.Bubblewrap == "" {
		options.Bubblewrap = defaultBubblewrap
	}

	if options.Confine == nil {
		options.Confine = confine.Apply
	}

	if err := findPrograms(&options); err != nil {
		return err
	}

	sockets, err := os.MkdirTemp("", "aibox-")
	if err != nil {
		return fmt.Errorf("create the socket folder: %w", err)
	}

	machine.Shares = withSockets(machine.Shares, sockets)

	died, stopDaemons, err := startDaemons(machine.Shares, machine.Owner, options)
	defer stopDaemons()

	if err != nil {
		_ = os.RemoveAll(sockets)

		return err
	}

	if err := waitForSockets(ctx, machine.Shares, options.SocketTimeout, died); err != nil {
		_ = os.RemoveAll(sockets)

		return err
	}

	vsock, err := options.OpenVsock()
	if err != nil {
		_ = os.RemoveAll(sockets)

		return err
	}

	files, err := openFiles(machine, options, vsock.Vhost)

	// the connections are made, and once confined aibox could not remove
	// the folder any more
	_ = os.RemoveAll(sockets)

	if err != nil {
		_ = vsock.Proxy.Close()
		_ = vsock.Terminal.Close()

		return err
	}

	defer files.close()

	logged := logConsole(files.console, options.ConsoleLog, options.Stderr)

	stopProxy := serveProxy(ctx, vsock.Proxy, options)
	defer stopProxy()

	machine.ProxyPort = vsock.ProxyPort

	stopTerminal := serveTerminal(ctx, vsock.Terminal, options)
	machine.TerminalPort = vsock.TerminalPort

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
	extra    []*os.File
	numbers  vm.Files
	kernelFD int
	console  *os.File
}

// openFiles opens everything QEMU needs, so that QEMU opens no path itself
// and a missing file is reported before it starts. The caller closes the
// extra files once QEMU has them, and the console when the log is done.
func openFiles(machine vm.Machine, options Options, vhost *os.File) (_ *qemuFiles, err error) {
	files := &qemuFiles{}
	files.numbers.Vhost = files.add(vhost)

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

	if files.kernelFD, err = files.open("kernel", machine.Kernel, os.O_RDONLY); err != nil {
		return nil, err
	}

	if files.numbers.Rootfs, err = files.open("root disk", machine.Rootfs, os.O_RDONLY); err != nil {
		return nil, err
	}

	if files.numbers.StateRead, err = files.open("state disk", machine.State, os.O_RDONLY); err != nil {
		return nil, err
	}

	state, err := lockedState(machine.State)
	if err != nil {
		return nil, err
	}

	files.numbers.StateWrite = files.add(state)

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

// lockedState opens the state disk for writing and locks it. The lock
// stays with the descriptor QEMU inherits, so a second run of the project
// fails here instead of writing to the same file system.
func lockedState(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // the path is the project's state disk
	if err != nil {
		return nil, fmt.Errorf("open the state disk: %w", err)
	}

	switch err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); {
	case errors.Is(err, unix.EWOULDBLOCK):
		_ = file.Close()

		return nil, fmt.Errorf("another aibox runs this project and has its state disk %s", path)
	case err != nil:
		_ = file.Close()

		return nil, fmt.Errorf("lock the state disk %s: %w", path, err)
	}

	return file, nil
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

// serveProxy serves the proxy to the VM on the listener until the returned
// function is called, which also waits for the proxy to stop.
func serveProxy(ctx context.Context, listener net.Listener, options Options) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)

	go func() { done <- proxy.Serve(ctx, forGuest(listener), options.Proxy) }()

	return func() {
		cancel()

		if err := <-done; err != nil {
			_, _ = fmt.Fprintf(options.Stderr, "aibox: the proxy stopped: %v\n", err)
		}
	}
}

// serveTerminal runs the session of the VM on the terminal of the person
// until the returned function is called. That function lets the session
// end, waits for the terminal to be restored and reports whether the VM
// ever connected.
func serveTerminal(ctx context.Context, listener net.Listener, options Options) func() bool {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan bool, 1)

	go func() { done <- attach(ctx, forGuest(listener), options) }()

	return func() bool {
		_ = listener.Close()

		select {
		case attached := <-done:
			return attached
		case <-time.After(sessionEndDelay):
			cancel()

			return <-done
		}
	}
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

// runQEMU starts QEMU, confines aibox itself, since QEMU was the last
// child it had to start, and waits for QEMU. The output of QEMU goes to
// Stderr, because Stdout belongs to the session.
func runQEMU(ctx context.Context, machine vm.Machine, files *qemuFiles, options Options) error {
	program, args := qemuCommand(machine, files, options)

	qemu := command(ctx, program, args)
	// through pipes, so that QEMU never holds the terminal
	qemu.Stdout = notAFile{options.Stderr}
	qemu.Stderr = notAFile{options.Stderr}
	qemu.ExtraFiles = files.extra

	err := qemu.Start()

	// QEMU has its own copies now, or never will, and the console log ends
	// only when every copy of its end is closed
	files.close()

	if err != nil {
		return fmt.Errorf("run qemu: %w", err)
	}

	if err := options.Confine(options.Ports); err != nil {
		_ = qemu.Cancel()

		return errors.Join(fmt.Errorf("confine aibox: %w", err), qemu.Wait())
	}

	if err := qemu.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return fmt.Errorf("run qemu: %w", err)
	}

	return nil
}

// findPrograms resolves QEMU and, unless the sandbox is off, bubblewrap,
// and checks that the host has what the sandbox binds in.
func findPrograms(options *Options) error {
	qemu, err := exec.LookPath(options.QEMU)
	if err != nil {
		return fmt.Errorf("find qemu: %w", err)
	}

	// bubblewrap binds the program at its own path, which must be absolute
	if options.QEMU, err = filepath.Abs(qemu); err != nil {
		return fmt.Errorf("find qemu: %w", err)
	}

	if options.NoSandbox {
		return nil
	}

	if options.Bubblewrap, err = exec.LookPath(options.Bubblewrap); err != nil {
		return fmt.Errorf("find bubblewrap for the sandbox of QEMU, or run with --no-sandbox: %w", err)
	}

	for _, path := range []string{libraries, firmware} {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("the sandbox of QEMU needs %s, run with --no-sandbox on this host: %w", path, err)
		}
	}

	return nil
}

// qemuCommand is QEMU in its sandbox, or QEMU alone without one. Outside
// the sandbox the kernel is read through /dev/fd.
func qemuCommand(machine vm.Machine, files *qemuFiles, options Options) (string, []string) {
	numbers := files.numbers

	if options.NoSandbox {
		numbers.Kernel = "/dev/fd/" + strconv.Itoa(files.kernelFD)

		return options.QEMU, machine.QEMUArgs(numbers)
	}

	numbers.Kernel = sandbox.Kernel

	spec := sandbox.Spec{
		Bubblewrap: options.Bubblewrap,
		Program:    options.QEMU,
		Libraries:  libraries,
		Firmware:   firmware,
		KernelFD:   files.kernelFD,
	}

	return spec.Command(machine.QEMUArgs(numbers))
}

// notAFile stops exec from handing the child the file itself.
type notAFile struct {
	io.Writer
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

// forGuest returns a listener that accepts the connections of the VM and
// closes all others. Only the VM is in its vsock namespace, so this is a
// check, not a filter.
func forGuest(listener net.Listener) net.Listener {
	return &guestListener{Listener: listener, cid: vm.GuestCID}
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
