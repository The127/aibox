// Package host serves the proxy and the terminal of a running VM, however
// the VM was started and however its listeners came about, and says how
// the run ended.
package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/session"
	"github.com/the127/aibox/internal/tunnel"
)

// DefaultSessionEndDelay is how long the session may go on after the VM
// stopped, to show its last output, unless a backend says otherwise.
const DefaultSessionEndDelay = 3 * time.Second

// DefaultBootTimeout is how long the guest of a VM the backend owns may
// take to connect, and DefaultPowerOffWait how long such a VM may take to
// power off after its session, before the backend stops it, unless the
// backend says otherwise.
const (
	DefaultBootTimeout  = time.Minute
	DefaultPowerOffWait = 10 * time.Second
)

// ErrNoTerminal is a VM that ended before the command in it connected,
// which the console log usually explains.
var ErrNoTerminal = errors.New("the VM ended before its terminal came up")

// ErrVMGone is a VM that ended while the command in it still ran, which
// the console log usually explains.
var ErrVMGone = errors.New("the VM ended while the command in it ran")

// Session is the terminal of the person on Stdin and Stdout, and the
// variables for the command in the VM, as NAME=value. EndDelay is how long
// the session may go on after the VM stopped, to show its last output. With
// Errors the session has no terminal: the command gets no input, what it
// prints goes to Stdout and its standard error to Errors.
type Session struct {
	Stdin    *os.File
	Stdout   io.Writer
	Errors   io.Writer
	Env      []string
	EndDelay time.Duration
	// OnPaste is called each time the person presses Ctrl+V, or is nil, as
	// session.Client takes it, and PasteAltV makes Alt+V a paste key too.
	OnPaste   func() []byte
	PasteAltV bool
	// Forwards are listeners whose connections go into the VM while the
	// sessions run. They are closed when the sessions are over.
	Forwards []backend.Forward
	// Then is a session without a terminal that runs once the first
	// session ended, or nil.
	Then *backend.Then
}

// Outcome is how the terminal session went: whether the VM connected, the
// exit code of the command in it, and why the session broke off.
type Outcome struct {
	attached bool
	code     int
	err      error
}

// ServeProxy serves the proxy to the VM on the listener until the returned
// function is called, which also waits for the proxy to stop. A proxy that
// breaks is reported on stderr. A closed listener is not: it means the end
// of the VM went away, as when the VM stops before the proxy does.
func ServeProxy(ctx context.Context, listener net.Listener, options proxy.Options, stderr io.Writer) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)

	go func() { done <- proxy.Serve(ctx, listener, options) }()

	return func() {
		cancel()

		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			_, _ = fmt.Fprintf(stderr, "aibox: the proxy stopped: %v\n", err)
		}
	}
}

// ServeTerminal runs the session of the VM on the terminal of the person
// until the returned function is called. That function lets the session
// end, waits for the terminal to be restored and reports how it went.
func ServeTerminal(ctx context.Context, listener net.Listener, s Session) func() Outcome {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan Outcome, 1)

	go func() { done <- attach(ctx, listener, s) }()

	return func() Outcome {
		_ = listener.Close()

		select {
		case ended := <-done:
			return ended
		case <-time.After(s.EndDelay):
			cancel()

			return <-done
		}
	}
}

// attach waits for the VM to connect and runs the session on the terminal
// of the person, then the session of Then. It reports whether the VM
// connected and the exit code of the command of the first session. The
// connection is closed when the context ends.
func attach(ctx context.Context, listener net.Listener, s Session) Outcome {
	conn, err := listener.Accept()
	if err != nil {
		return Outcome{}
	}

	defer func() { _ = conn.Close() }()

	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var forwardAll func(ctx context.Context, dial session.Dial)
	if len(s.Forwards) > 0 {
		forwardAll = func(ctx context.Context, dial session.Dial) {
			forward(ctx, s.Forwards, dial)
		}
	}

	c, err := session.Connect(conn, forwardAll)
	if err != nil {
		return Outcome{attached: true, err: err}
	}

	defer func() { _ = c.Close() }()

	ended := runFirst(ctx, c, s)
	if ended.err != nil || s.Then == nil || ctx.Err() != nil {
		return ended
	}

	// the command of Then says how it went in what it prints
	then := session.Exec{Out: s.Then.Stdout, Errors: s.Then.Stderr}
	if _, err := then.Run(c); err != nil && ctx.Err() == nil {
		ended.err = err
	}

	return ended
}

// runFirst runs the first session, on the terminal of the person or, with
// Errors, without one.
func runFirst(ctx context.Context, c *session.Conn, s Session) Outcome {
	if s.Errors != nil {
		client := session.Exec{In: strings.NewReader(""), Out: s.Stdout, Errors: s.Errors, Env: s.Env}

		code, err := client.Run(c)
		if err != nil && ctx.Err() == nil {
			return Outcome{attached: true, err: err}
		}

		return Outcome{attached: true, code: code}
	}

	client, restore, err := session.NewClient(s.Stdin, s.Stdout)
	if err != nil {
		return Outcome{attached: true, err: fmt.Errorf("prepare the terminal: %w", err)}
	}

	client.Env = s.Env
	client.OnPaste = s.OnPaste
	client.PasteAltV = s.PasteAltV

	code, err := client.Attach(c)

	restore()

	if err != nil && ctx.Err() == nil {
		return Outcome{attached: true, err: err}
	}

	return Outcome{attached: true, code: code}
}

// forward passes each connection of the listeners on to its port in the VM
// until the context ends, and closes the listeners then.
func forward(ctx context.Context, forwards []backend.Forward, dial session.Dial) {
	var served sync.WaitGroup

	for _, f := range forwards {
		served.Go(func() {
			_ = tunnel.Serve(ctx, f.Listener, func(ctx context.Context, conn net.Conn) {
				vm, err := dial(f.Port)
				if err != nil {
					return
				}

				defer func() { _ = vm.Close() }()

				stop := context.AfterFunc(ctx, func() { _ = vm.Close() })
				defer stop()

				tunnel.Join(conn, vm)
			})
		})
	}

	served.Wait()
}

// MaxConsoleLogBytes is how much of the console of a VM goes into its log.
const MaxConsoleLogBytes = 16 << 20

// ConsoleLog passes the first MaxConsoleLogBytes on to w and drops the
// rest, so that the VM cannot fill the disk of the host through its
// console. It never fails, so that the console is read to its end.
func ConsoleLog(w io.Writer) io.Writer {
	return &capped{w: w, left: MaxConsoleLogBytes}
}

type capped struct {
	w    io.Writer
	left int64
}

func (c *capped) Write(b []byte) (int, error) {
	n := len(b)

	if int64(len(b)) > c.left {
		b = b[:c.left]
	}

	c.left -= int64(len(b))

	if len(b) > 0 {
		_, _ = c.w.Write(b)
	}

	return n, nil
}

// Result is how the run ended: the error of the VM itself, a VM that never
// connected or one that ended while its command ran, with the console log
// that likely says why, the error of the session, or the exit code of the
// command in the VM.
func Result(vmErr error, ended Outcome, consoleLog string) error {
	switch {
	case vmErr != nil:
		return vmErr
	case !ended.attached && consoleLog != "":
		return fmt.Errorf("%w, see %s", ErrNoTerminal, consoleLog)
	case !ended.attached:
		return ErrNoTerminal
	case errors.Is(ended.err, session.ErrNoExit) && consoleLog != "":
		return fmt.Errorf("%w, see %s", ErrVMGone, consoleLog)
	case errors.Is(ended.err, session.ErrNoExit):
		return ErrVMGone
	case ended.err != nil:
		return ended.err
	case ended.code != 0:
		return &backend.ExitError{Code: ended.code}
	}

	return nil
}
