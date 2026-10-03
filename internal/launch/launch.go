// Package launch runs the aibox VM: virtiofsd for each share, then QEMU.
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/the127/aibox/internal/vm"
)

// ErrSocketTimeout is returned when virtiofsd does not create its socket in
// time.
var ErrSocketTimeout = errors.New("virtiofsd did not create its socket in time")

const (
	defaultSocketTimeout = 10 * time.Second
	stopDelay            = time.Second
)

// Options are the programs Run starts and where QEMU reads and writes.
// Stdin is a file, so that QEMU gets the terminal itself. A zero
// SocketTimeout means ten seconds.
type Options struct {
	QEMU          string
	Virtiofsd     string
	Stdin         *os.File
	Stdout        io.Writer
	Stderr        io.Writer
	SocketTimeout time.Duration
}

// Run boots the machine and returns when QEMU exits, or with the error of
// the context when it ends first. On return the virtiofsd processes are
// stopped and their sockets are removed.
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

	daemonCtx, stopDaemons := context.WithCancel(ctx)

	var running sync.WaitGroup

	defer func() {
		stopDaemons()
		running.Wait()
	}()

	died := make(chan error, len(machine.Shares))

	for _, share := range machine.Shares {
		daemon := command(daemonCtx, options.Virtiofsd, share.VirtiofsdArgs())
		daemon.Stderr = options.Stderr
		// a Ctrl-C from the terminal reaches QEMU alone
		daemon.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		if err := daemon.Start(); err != nil {
			return fmt.Errorf("start virtiofsd for %s: %w", share.Dir, err)
		}

		running.Add(1)

		go func() {
			defer running.Done()

			died <- fmt.Errorf("virtiofsd for %s exited: %w", share.Dir, daemon.Wait())
		}()
	}

	if err := waitForSockets(ctx, machine.Shares, options.SocketTimeout, died); err != nil {
		return err
	}

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
