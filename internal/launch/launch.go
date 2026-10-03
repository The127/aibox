// Package launch runs the aibox VM: virtiofsd for each share, the proxy for
// the VM, then QEMU.
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

	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/vm"
)

// ErrSocketTimeout is returned when virtiofsd does not create its socket in
// time.
var ErrSocketTimeout = errors.New("virtiofsd did not create its socket in time")

const (
	defaultSocketTimeout = 10 * time.Second
	stopDelay            = time.Second
)

// Options are the programs Run starts, where QEMU reads and writes, and how
// the VM reaches the proxy.
type Options struct {
	QEMU      string
	Virtiofsd string
	Stdin     *os.File
	Stdout    io.Writer
	Stderr    io.Writer
	// SocketTimeout is how long virtiofsd may take to create its socket.
	// Zero means ten seconds.
	SocketTimeout time.Duration
	// ListenVsock opens the listener the VM reaches the proxy on and
	// returns its port. Nil listens on vsock.
	ListenVsock func() (net.Listener, uint32, error)
	Proxy       proxy.Options
}

// Run boots the machine with the proxy listening for it and returns when
// QEMU exits, or with the error of the context when it ends first. On return
// the proxy and the virtiofsd processes are stopped and their sockets are
// removed.
func Run(ctx context.Context, machine vm.Machine, options Options) error {
	if options.SocketTimeout == 0 {
		options.SocketTimeout = defaultSocketTimeout
	}

	sockets, err := os.MkdirTemp("", "aibox-")
	if err != nil {
		return fmt.Errorf("create the socket folder: %w", err)
	}

	defer func() { _ = os.RemoveAll(sockets) }()

	machine.Shares = withSockets(machine.Shares, sockets)

	died, stopDaemons, err := startDaemons(machine.Shares, options)
	defer stopDaemons()

	if err != nil {
		return err
	}

	if err := waitForSockets(ctx, machine.Shares, options.SocketTimeout, died); err != nil {
		return err
	}

	port, stopProxy, err := serveProxy(ctx, machine.GuestCID, options)
	if err != nil {
		return err
	}

	defer stopProxy()

	machine.ProxyPort = port

	return runQEMU(ctx, machine, options)
}

// startDaemons starts virtiofsd for each share. The channel gets the exit
// of each daemon, and the returned function stops the daemons and waits
// for them.
func startDaemons(shares []vm.Share, options Options) (<-chan error, func(), error) {
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
		daemon := command(ctx, options.Virtiofsd, share.VirtiofsdArgs())
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
	listen := options.ListenVsock
	if listen == nil {
		listen = listenVsock
	}

	listener, port, err := listen()
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

func runQEMU(ctx context.Context, machine vm.Machine, options Options) error {
	qemu := command(ctx, options.QEMU, machine.QEMUArgs())
	qemu.Stdin = options.Stdin
	qemu.Stdout = options.Stdout
	qemu.Stderr = options.Stderr

	if err := qemu.Run(); err != nil {
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
