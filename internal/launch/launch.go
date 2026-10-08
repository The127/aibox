//go:build linux

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
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/confine"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/sandbox"
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
	defaultLibraries     = "/usr/lib64"
	defaultFirmware      = "/usr/share/qemu/qboot.rom"
	defaultStopDelay     = time.Second
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
	// Progress gets the standard error of a session without a terminal,
	// which a task has. Nil runs the session on the terminal.
	Progress io.Writer
	// RemoveState removes the state disk once QEMU has it open.
	RemoveState bool
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
	// Libraries and Firmware are what QEMU is made of on the host, bound
	// into its sandbox. Empty means /usr/lib64 and qboot.rom of QEMU.
	Libraries string
	Firmware  string
	// StopDelay is how long a stopped program may take before it is
	// killed, and SessionEndDelay how long the session may go on after
	// QEMU exited, to show the last output of the VM. Zero means the
	// default.
	StopDelay       time.Duration
	SessionEndDelay time.Duration
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

	if options.Libraries == "" {
		options.Libraries = defaultLibraries
	}

	if options.Firmware == "" {
		options.Firmware = defaultFirmware
	}

	if options.StopDelay == 0 {
		options.StopDelay = defaultStopDelay
	}

	if options.SessionEndDelay == 0 {
		options.SessionEndDelay = host.DefaultSessionEndDelay
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

	// QEMU gets the disk by its descriptor, which keeps the file until
	// the VM is gone
	if options.RemoveState {
		if err := os.Remove(machine.State); err != nil {
			files.close()

			_ = vsock.Proxy.Close()
			_ = vsock.Terminal.Close()

			return fmt.Errorf("remove the state disk: %w", err)
		}
	}

	defer files.close()

	logged := logConsole(files.console, options.ConsoleLog, options.Stderr)

	stopProxy := host.ServeProxy(ctx, forGuest(vsock.Proxy), options.Proxy, options.Stderr)
	defer stopProxy()

	machine.ProxyPort = vsock.ProxyPort

	stopTerminal := host.ServeTerminal(ctx, forGuest(vsock.Terminal), host.Session{
		Stdin:    options.Stdin,
		Stdout:   options.Stdout,
		Errors:   options.Progress,
		Env:      options.Env,
		EndDelay: options.SessionEndDelay,
	})
	machine.TerminalPort = vsock.TerminalPort

	err = runQEMU(ctx, machine, files, options)

	<-logged

	return host.Result(err, stopTerminal(), options.ConsoleLog)
}

// ErrNoTerminal is a VM that ended before the command in it connected,
// which the console log usually explains.
var ErrNoTerminal = host.ErrNoTerminal

// ExitError is a command in the VM that ended with a code other than 0.
type ExitError = backend.ExitError

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
func openFiles(m vm.Machine, options Options, vhost *os.File) (_ *qemuFiles, err error) {
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

	if files.kernelFD, err = files.open("kernel", m.Kernel, os.O_RDONLY); err != nil {
		return nil, err
	}

	if files.numbers.Rootfs, err = files.open("root disk", m.Rootfs, os.O_RDONLY); err != nil {
		return nil, err
	}

	if files.numbers.StateRead, err = files.open("state disk", m.State, os.O_RDONLY); err != nil {
		return nil, err
	}

	state, err := machine.LockState(m.State)
	if err != nil {
		return nil, err
	}

	files.numbers.StateWrite = files.add(state)

	for _, share := range m.Shares {
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

		_, _ = io.Copy(host.ConsoleLog(log), console)
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
		daemon := command(ctx, options.Virtiofsd, share.VirtiofsdArgs(owner), options.StopDelay)
		daemon.Stderr = options.Stderr
		// a Ctrl-C from the terminal reaches QEMU alone, and the kernel
		// kills the daemon when aibox dies without stopping it
		daemon.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}

		if err := start(daemon); err != nil {
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

// runQEMU starts QEMU, confines aibox itself, since QEMU was the last
// child it had to start, and waits for QEMU. The output of QEMU goes to
// Stderr, because Stdout belongs to the session.
func runQEMU(ctx context.Context, machine vm.Machine, files *qemuFiles, options Options) error {
	program, args := qemuCommand(machine, files, options)

	qemu := command(ctx, program, args, options.StopDelay)
	// through pipes, so that QEMU never holds the terminal
	qemu.Stdout = notAFile{options.Stderr}
	qemu.Stderr = notAFile{options.Stderr}
	qemu.ExtraFiles = files.extra

	// bubblewrap kills QEMU in the sandbox when aibox dies, with
	// --die-with-parent. Without the sandbox the kernel does it
	if options.NoSandbox {
		qemu.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	}

	err := start(qemu)

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

	for _, path := range []string{options.Libraries, options.Firmware} {
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
		Libraries:  options.Libraries,
		Firmware:   options.Firmware,
		KernelFD:   files.kernelFD,
	}

	return spec.Command(machine.QEMUArgs(numbers))
}

// notAFile stops exec from handing the child the file itself.
type notAFile struct {
	io.Writer
}

// command stops the program with SIGTERM when the context ends and kills it
// when it has not exited after the delay.
func command(ctx context.Context, program string, args []string, delay time.Duration) *exec.Cmd {
	cmd := exec.CommandContext(ctx, program, args...) //nolint:gosec // the caller chooses the program
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = delay

	return cmd
}

// starts carries the work of the starter, the goroutine that starts every
// child of aibox. See start.
var (
	starts      = make(chan func())
	starterOnce sync.Once
)

// start starts the command on a thread that lives as long as aibox does.
//
// The kernel sends the Pdeathsig of a child when the thread that started it
// ends, not when aibox ends. Go ends a thread when a goroutine locked to it
// returns, and any goroutine may run on any thread, so a child started from
// a goroutine could be killed while aibox still runs. bubblewrap's
// --die-with-parent works the same way, so this matters for the sandbox of
// QEMU too.
//
// The starter locks its thread and never returns, so Go never ends that
// thread and no other goroutine runs on it. The thread ends only with
// aibox, and with it the children. This costs one idle thread. Doing the
// whole of Run on a locked thread would not do, since the caller decides
// which thread Run runs on and may end it.
func start(cmd *exec.Cmd) error {
	starterOnce.Do(func() {
		go func() {
			runtime.LockOSThread()

			for work := range starts {
				work()
			}
		}()
	})

	started := make(chan error, 1)
	starts <- func() { started <- cmd.Start() }

	return <-started
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
