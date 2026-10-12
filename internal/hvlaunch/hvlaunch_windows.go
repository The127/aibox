package hvlaunch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio/pkg/guid"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/appcontainer"
	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/hvservice"
	"github.com/the127/aibox/internal/hvshares"
	"github.com/the127/aibox/internal/hvsock"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/ninefs"
	"github.com/the127/aibox/internal/vm"
)

const (
	// the ports of the VM the guest dials the host on. Only aibox can
	// listen on the sockets of its VM, so fixed ports are no one else's.
	terminalPort = 1024
	proxyPort    = 1025
	// sharesPort is where the guest mounts its 9p shares from.
	sharesPort = 564

	// baseCmdline boots the root disk at LUN 0, which Linux names first,
	// read-only, with the console on the serial port Hyper-V sends to the
	// pipe. A panicking guest stops, which the VM's document turns into
	// the end of the VM.
	baseCmdline = "root=/dev/sda rootfstype=ext4 ro rootwait console=ttyS0,115200 pci=off panic=-1 quiet"
)

var (
	// ErrNoBoot is a VM whose guest did not connect in time.
	ErrNoBoot = errors.New("the VM did not come up")
	// ErrStartedOver is a guest that connected again, after it started
	// over.
	ErrStartedOver = errors.New("the guest started over")
	// ErrSharesGone is a helper that served the folders of the VM and
	// ended, after which the VM has them no more.
	ErrSharesGone = errors.New("the helper that serves the folders of the VM ended")
)

// Backend runs the VM through the service. BootTimeout is how long the
// guest may take to connect, SessionEndDelay how long the session may go
// on after the VM stopped and PowerOffWait how long the VM may take to
// power off after the session before it is stopped.
type Backend struct {
	BootTimeout     time.Duration
	SessionEndDelay time.Duration
	PowerOffWait    time.Duration

	// dial connects to the service, listen listens on a port of the VM,
	// console takes the pipe of its console under the handle the service
	// put into this process and shares serves the folders of the VM from a
	// helper in the job, which the tests fake
	dial    func(ctx context.Context) (net.Conn, error)
	listen  func(vm guid.GUID, port uint32) (net.Listener, error)
	console func(handle uint64) consoleSource
	shares  func(ctx context.Context, job *appcontainer.Job, stderr io.Writer, c hvshares.Config) (helper, error)
}

var _ backend.Backend = Backend{}

// helper is a helper of the run, which ends with Close, and which is Done
// when it ended before.
type helper interface {
	io.Closer
	Done() <-chan struct{}
}

// NewBackend returns the Backend with its default timeouts.
func NewBackend() Backend {
	return Backend{
		BootTimeout:     host.DefaultBootTimeout,
		SessionEndDelay: host.DefaultSessionEndDelay,
		PowerOffWait:    host.DefaultPowerOffWait,
		dial:            hvservice.Dial,
		listen:          hvsock.Listen,
		console:         consoleOf,
		shares:          startShares,
	}
}

// Run boots the VM of the spec through the service, with the shares, the
// proxy and the terminal served to it, and returns when it ended, or once
// it was stopped because the context ended, the guest did not connect in
// time or it stayed up after its session.
func (b Backend) Run(ctx context.Context, spec backend.Spec) error {
	if spec.Stderr == nil {
		spec.Stderr = io.Discard
	}

	// before the disk is prepared, which takes it to write: a disk that
	// isn't there yet is no one's
	if err := checkNotBusy(spec.State); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// before the machine is prepared, which would make the disk a sparse
	// raw file, which Hyper-V refuses; and after the image is checked, as
	// Prepare makes the disk, so that a run with no image leaves none
	if _, _, err := machine.Image(spec.Image); err != nil {
		return err
	}

	if err := prepareState(spec.State, spec.StateBytes); err != nil {
		return err
	}

	m, err := machine.Prepare(spec)
	if err != nil {
		return err
	}

	m.HyperV = true
	m.TerminalPort = terminalPort
	m.ProxyPort = proxyPort

	cmdline := m.Cmdline(baseCmdline)
	if len(cmdline) > hvservice.MaxCmdline {
		return fmt.Errorf("the kernel command line is %d bytes, more than the %d the kernel takes: fewer mounts would make it shorter", len(cmdline), hvservice.MaxCmdline)
	}

	// the helper of the shares finds out only once the VM is made
	if err := checkShares(m.Shares); err != nil {
		return err
	}

	// the helpers of the run end with it, or with aibox, however it ends
	job, err := appcontainer.NewJob()
	if err != nil {
		return err
	}

	defer func() { _ = job.Close() }()

	state, err := openState(m.State)
	if err != nil {
		return err
	}

	defer func() { _ = windows.CloseHandle(state) }()

	s, err := b.connect(ctx)
	if err != nil {
		return err
	}

	defer s.close()

	created, err := s.ask(hvservice.Request{Op: hvservice.OpCreate, Create: &hvservice.Create{
		MemoryMiB: m.MemoryMiB,
		CPUs:      m.CPUs,
		Cmdline:   cmdline,
		State:     uint64(state),
	}})
	if err != nil {
		return fmt.Errorf("create the VM: %w", err)
	}

	id, err := guid.FromString(created.RuntimeID)
	if err != nil {
		return fmt.Errorf("read the id of the VM: %w", err)
	}

	// the pipe is this process's from the reply on, whatever comes of the run
	console := b.console(created.Console)
	defer func() { _ = console.Close() }()

	log, err := openLog(spec.ConsoleLog)
	if err != nil {
		return err
	}

	defer func() { _ = log.Close() }()

	// before the start, as Hyper-V connects the console as the VM starts,
	// and a pipe no one listens on turns it away
	if err := console.Listen(); err != nil {
		return err
	}

	gone := serveConsole(console, host.ConsoleLog(log))

	shares, err := b.shares(ctx, job, spec.Stderr, hvshares.Config{
		VM:     id,
		Port:   sharesPort,
		Shares: sharesOf(m.Shares),
		Owner:  ninefs.Owner{UID: vm.GuestUID, GID: vm.GuestGID},
	})
	if err != nil {
		return err
	}

	defer func() { _ = shares.Close() }()

	proxyListener, err := b.listen(id, proxyPort)
	if err != nil {
		return err
	}

	stopProxy := host.ServeProxy(ctx, proxyListener, spec.Proxy, spec.Stderr)
	defer stopProxy()

	terminalListener, err := b.listen(id, terminalPort)
	if err != nil {
		return err
	}

	terminal := host.Watch(terminalListener)
	stopTerminal := host.ServeTerminal(ctx, terminal, host.Session{
		Stdin:    spec.Stdin,
		Stdout:   spec.Stdout,
		Errors:   spec.Progress,
		Env:      spec.Env,
		EndDelay: b.SessionEndDelay,
		OnPaste:  spec.OnPaste,
	})

	// the service ends a VM that did not start, so there is none to stop
	if _, err := s.ask(hvservice.Request{Op: hvservice.OpStart}); err != nil {
		return host.Result(fmt.Errorf("start the VM: %w", err), stopTerminal(), spec.ConsoleLog)
	}

	// the VM has the disk open, which keeps the data until it is gone. A
	// VM whose open shares no delete keeps the name too, until then.
	removeLater := false

	if spec.RemoveState {
		if err := unlink(m.State); err != nil {
			_, _ = fmt.Fprintf(spec.Stderr, "aibox: the state disk stays until the VM is gone: %v\n", err)

			removeLater = true
		}
	}

	vmErr := b.wait(ctx, gone, shares.Done(), terminal, s.stop)

	// the VM is gone, so Hyper-V has let go of the console, whose last
	// lines may still be on their way into the log: they are what explains
	// a VM that did not come up. A console Hyper-V never connected ends
	// with its pipe.
	_ = console.Close()
	<-gone

	if removeLater {
		if err := os.Remove(m.State); err != nil {
			vmErr = errors.Join(vmErr, fmt.Errorf("remove the state disk: %w", err))
		}
	}

	if (errors.Is(vmErr, ErrNoBoot) || errors.Is(vmErr, ErrStartedOver)) && spec.ConsoleLog != "" {
		vmErr = fmt.Errorf("%w, see %s", vmErr, spec.ConsoleLog)
	}

	return host.Result(vmErr, stopTerminal(), spec.ConsoleLog)
}

// wait waits for the VM to be gone by itself, and stops it when the
// context ends, the guest does not connect in time, starts over or the VM
// stays up after the session.
func (b Backend) wait(ctx context.Context, gone, sharesGone <-chan struct{}, terminal *host.Watched, stop func() error) error {
	boot := time.NewTimer(b.BootTimeout)
	defer boot.Stop()

	connected, ended := terminal.Connected, terminal.Ended

	var afterSession <-chan time.Time

	for {
		select {
		case <-gone:
			// the service still holds the VM that ended, until told
			return stop()
		case <-connected:
			boot.Stop()

			connected = nil
		case <-ended:
			afterSession = time.After(b.PowerOffWait)
			ended = nil
		case <-afterSession:
			return stop()
		case <-terminal.Again:
			return errors.Join(ErrStartedOver, stop())
		case <-sharesGone:
			return errors.Join(ErrSharesGone, stop())
		case <-boot.C:
			return errors.Join(fmt.Errorf("%w in %v", ErrNoBoot, b.BootTimeout), stop())
		case <-ctx.Done():
			return errors.Join(ctx.Err(), stop())
		}
	}
}

// service is a connection to the service, for the one VM of the run.
type service struct {
	conn net.Conn
	r    *bufio.Reader
}

func (b Backend) connect(ctx context.Context) (*service, error) {
	conn, err := b.dial(ctx)
	if err != nil {
		return nil, err
	}

	return &service{conn: conn, r: hvservice.NewReader(conn)}, nil
}

// ask sends the request and returns the reply, or what the service
// refused it for.
func (s *service) ask(request hvservice.Request) (hvservice.Reply, error) {
	if err := hvservice.Write(s.conn, request); err != nil {
		return hvservice.Reply{}, err
	}

	var reply hvservice.Reply
	if err := hvservice.Read(s.r, &reply); err != nil {
		return hvservice.Reply{}, err
	}

	if reply.Error != "" {
		return hvservice.Reply{}, errors.New(reply.Error)
	}

	return reply, nil
}

// stop ends the VM, and returns once it is gone.
func (s *service) stop() error {
	if _, err := s.ask(hvservice.Request{Op: hvservice.OpStop}); err != nil {
		return fmt.Errorf("stop the VM: %w", err)
	}

	return nil
}

// close ends the connection, which ends a VM that still runs.
func (s *service) close() {
	_ = s.conn.Close()
}

// sharesOf are the shares as ninefs serves them.
func sharesOf(shares []vm.Share) []ninefs.Share {
	served := make([]ninefs.Share, len(shares))
	for i, s := range shares {
		served[i] = ninefs.Share{Name: s.Tag, Dir: s.Dir, ReadOnly: s.IsReadOnly()}
	}

	return served
}

// openLog opens the console log at path, or nowhere if path is empty. It
// is opened once the service has the VM, so that a run the service
// refuses keeps the log of the one before.
func openLog(path string) (io.WriteCloser, error) {
	if path == "" {
		return nopCloser{io.Discard}, nil
	}

	log, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the path is the project's console log
	if err != nil {
		return nil, fmt.Errorf("open the console log: %w", err)
	}

	return log, nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// startShares starts the helper that serves the folders of the VM: aibox
// itself, under the command of the helper.
func startShares(ctx context.Context, job *appcontainer.Job, stderr io.Writer, c hvshares.Config) (helper, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find aibox for the helper: %w", err)
	}

	return hvshares.Start(ctx, self, job, stderr, c)
}

// checkShares refuses a share whose folder is none, before the run makes
// anything for the VM.
func checkShares(shares []vm.Share) error {
	for _, s := range shares {
		info, err := os.Stat(s.Dir)
		if err != nil {
			return fmt.Errorf("share %s: %w", s.Tag, err)
		}

		if !info.IsDir() {
			return fmt.Errorf("share %s: %s is no folder", s.Tag, s.Dir)
		}
	}

	return nil
}
