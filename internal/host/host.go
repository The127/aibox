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
	"time"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/session"
)

// DefaultSessionEndDelay is how long the session may go on after the VM
// stopped, to show its last output, unless a backend says otherwise.
const DefaultSessionEndDelay = 3 * time.Second

// ErrNoTerminal is a VM that ended before the command in it connected,
// which the console log usually explains.
var ErrNoTerminal = errors.New("the VM ended before its terminal came up")

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
// of the person. It reports whether the VM connected and the exit code of
// the command. The connection is closed when the context ends.
func attach(ctx context.Context, listener net.Listener, s Session) Outcome {
	conn, err := listener.Accept()
	if err != nil {
		return Outcome{}
	}

	defer func() { _ = conn.Close() }()

	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if s.Errors != nil {
		client := session.Exec{In: strings.NewReader(""), Out: s.Stdout, Errors: s.Errors, Env: s.Env}

		code, err := client.Run(conn)
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

	code, err := client.Attach(conn)

	restore()

	if err != nil && ctx.Err() == nil {
		return Outcome{attached: true, err: err}
	}

	return Outcome{attached: true, code: code}
}

// Result is how the run ended: the error of the VM itself, a VM that never
// connected, with the console log that likely says why, the error of the
// session, or the exit code of the command in the VM.
func Result(vmErr error, ended Outcome, consoleLog string) error {
	switch {
	case vmErr != nil:
		return vmErr
	case !ended.attached && consoleLog != "":
		return fmt.Errorf("%w, see %s", ErrNoTerminal, consoleLog)
	case !ended.attached:
		return ErrNoTerminal
	case ended.err != nil:
		return ended.err
	case ended.code != 0:
		return &backend.ExitError{Code: ended.code}
	}

	return nil
}
