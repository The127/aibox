package hvserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

// ServiceName is the name the service is installed under, and the source
// of its events in the Application event log.
const ServiceName = "aibox"

// The files the service boots every VM from, in its folder.
const (
	KernelFile = "vmlinuz-hyperv"
	RootFile   = "os.vhd"
)

// eventID is the id of every event of the service: they say in words what
// happened, and nothing reads them by number.
const eventID = 1

// failed is the service's own exit code for a service that ended other
// than as the manager stopped it, the event log saying why.
const failed = 1

// Folder is the folder of the files the service boots VMs from, under
// ProgramData. Its install makes it so that only Administrators and
// SYSTEM may change it, as the service boots what it holds as SYSTEM.
func Folder() (string, error) {
	data, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", fmt.Errorf("find ProgramData: %w", err)
	}

	return filepath.Join(data, "aibox"), nil
}

// Run runs the service as Windows' service manager starts it, until the
// manager stops it.
func Run() error {
	elog, err := eventlog.Open(ServiceName)
	if err != nil {
		return fmt.Errorf("open the event log: %w", err)
	}

	defer func() { _ = elog.Close() }()

	logf := func(format string, args ...any) { _ = elog.Info(eventID, fmt.Sprintf(format, args...)) }

	h := &handler{
		serve: func(ctx context.Context, ready func()) error { return serve(ctx, ready, logf) },
		fail:  func(err error) { _ = elog.Error(eventID, err.Error()) },
	}

	if err := svc.Run(ServiceName, h); err != nil {
		return fmt.Errorf("run the service: %w", err)
	}

	return nil
}

// serve ends the VMs a service before left behind, listens on the pipe,
// says it is ready, and serves until ctx ends.
func serve(ctx context.Context, ready func(), logf func(format string, args ...any)) error {
	folder, err := Folder()
	if err != nil {
		return err
	}

	// before the pipe, so that it ends none of this service's VMs
	sweep(ctx, leftBehind, endVM, logf)

	// the containers of VMs a service that crashed left on the loopback
	exempt := newExemptions()
	if err := exempt.sweep(); err != nil {
		logf("take the containers of earlier VMs off the loopback: %v", err)
	}

	l, err := listen(hvservice.PipeName, hvservice.PipeSDDL)
	if err != nil {
		return err
	}

	srv := &server{
		install: hvservice.Install{Kernel: filepath.Join(folder, KernelFile), Rootfs: filepath.Join(folder, RootFile)},
		host:    host,
		create:  createVM,
		logf:    logf,

		containerSID: hvservice.ContainerSID,
		network:      filterNetwork,
		exempt:       exempt.exempt,
	}

	ready()
	logf("the service runs, on %s", hvservice.PipeName)

	// the VMs get at state disks through the Host Compute Service, which
	// keeps the entries for their accounts
	disks := stateDisks{grant: hcs.GrantVMAccess, revoke: hcs.RevokeVMAccess}

	err = srv.run(ctx, l, func(conn net.Conn) peer { return peerOf(conn, disks) })
	logf("the service stopped, every VM of it ended")

	return err
}

// handler tells Windows' service manager how the service is: starting
// until serve is ready, then running until the manager stops it or the
// machine shuts down, then stopping until every VM has ended.
type handler struct {
	serve func(ctx context.Context, ready func()) error
	fail  func(error)
}

// Execute implements svc.Handler.
func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	ready := make(chan struct{})
	done := make(chan error, 1)

	go func() { done <- h.serve(ctx, func() { close(ready) }) }()

	accepts := svc.AcceptStop | svc.AcceptShutdown

	for {
		select {
		case <-ready:
			ready = nil
			status <- svc.Status{State: svc.Running, Accepts: accepts}
		case err := <-done:
			// the service ends only as the manager stops it, so serve
			// returning on its own is a failure, whatever it says
			if err == nil {
				err = errNoReason
			}

			h.fail(fmt.Errorf("the service ended: %w", err))

			return true, failed
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				stop()

				if err := <-done; err != nil {
					h.fail(err)
				}

				return false, 0
			default:
				// the service accepts no other command
			}
		}
	}
}

// errNoReason is the failure of a service that ended unasked and said
// nothing of why.
var errNoReason = errors.New("for no reason it gave")
