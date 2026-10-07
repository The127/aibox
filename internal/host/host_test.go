package host_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/session"
)

// process is a command in the VM that prints its output and exits.
type process struct {
	output io.Reader
	code   int
}

func (p *process) Read(b []byte) (int, error)  { return p.output.Read(b) }
func (p *process) Write(b []byte) (int, error) { return len(b), nil }
func (p *process) Resize(session.Size) error   { return nil }
func (p *process) Close() error                { return nil }
func (p *process) Wait() (int, error)          { return p.code, nil }

// syncBuffer is the terminal of the person, written by the session.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func listen(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	return listener
}

// guest connects to the listener and serves a session whose command prints
// the output and exits with the code.
func guest(t *testing.T, listener net.Listener, output string, code int) <-chan error {
	t.Helper()

	served := make(chan error, 1)

	go func() {
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			served <- err

			return
		}

		defer func() { _ = conn.Close() }()

		served <- session.Serve(conn, func(session.Request) (session.Process, error) {
			return &process{output: strings.NewReader(output), code: code}, nil
		})
	}()

	return served
}

func TestTheTerminalShowsTheSessionAndEndsWithItsExitCode(t *testing.T) {
	// arrange
	listener := listen(t)
	screen := &syncBuffer{}
	end := host.ServeTerminal(context.Background(), listener, host.Session{Stdout: screen, EndDelay: 5 * time.Second})
	served := guest(t, listener, "hello from the VM", 3)

	// act
	require.NoError(t, <-served)
	ended := end()

	// assert
	assert.Equal(t, &backend.ExitError{Code: 3}, host.Result(nil, ended, ""))
	assert.Contains(t, screen.String(), "hello from the VM")
}

// pipedProcess is a command without a terminal that prints to its
// standard output and its standard error.
type pipedProcess struct {
	process

	stderr io.Reader
}

func (p *pipedProcess) Stderr() io.Reader { return p.stderr }
func (p *pipedProcess) CloseInput() error { return nil }

func TestASessionWithoutATerminalKeepsStandardErrorApart(t *testing.T) {
	// arrange
	listener := listen(t)
	out, errs := &syncBuffer{}, &syncBuffer{}
	end := host.ServeTerminal(context.Background(), listener, host.Session{Stdout: out, Errors: errs, EndDelay: 5 * time.Second})

	requests := make(chan session.Request, 1)
	served := make(chan error, 1)

	go func() {
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			served <- err

			return
		}

		defer func() { _ = conn.Close() }()

		served <- session.Serve(conn, func(request session.Request) (session.Process, error) {
			requests <- request

			return &pipedProcess{process: process{output: strings.NewReader("results"), code: 1}, stderr: strings.NewReader("progress")}, nil
		})
	}()

	// act
	require.NoError(t, <-served)
	ended := end()

	// assert
	assert.Equal(t, &backend.ExitError{Code: 1}, host.Result(nil, ended, ""))
	assert.False(t, (<-requests).Terminal)
	assert.Equal(t, "results", out.String())
	assert.Equal(t, "progress", errs.String())
}

func TestASessionThatEndsWellIsNoError(t *testing.T) {
	// arrange
	listener := listen(t)
	end := host.ServeTerminal(context.Background(), listener, host.Session{Stdout: io.Discard, EndDelay: 5 * time.Second})
	served := guest(t, listener, "", 0)

	// act
	require.NoError(t, <-served)
	ended := end()

	// assert
	assert.NoError(t, host.Result(nil, ended, ""))
}

func TestAVMThatNeverConnectedEndsWithoutATerminal(t *testing.T) {
	// arrange
	end := host.ServeTerminal(context.Background(), listen(t), host.Session{Stdout: io.Discard, EndDelay: time.Second})

	// act
	ended := end()

	// assert
	assert.ErrorIs(t, host.Result(nil, ended, ""), host.ErrNoTerminal)
	assert.ErrorContains(t, host.Result(nil, ended, "/p/console.log"), "see /p/console.log")
}

func TestTheErrorOfTheVMComesFirst(t *testing.T) {
	// arrange
	end := host.ServeTerminal(context.Background(), listen(t), host.Session{Stdout: io.Discard, EndDelay: time.Second})
	ended := end()
	failed := errors.New("qemu failed")

	// act
	err := host.Result(failed, ended, "/p/console.log")

	// assert
	assert.ErrorIs(t, err, failed)
	assert.NotErrorIs(t, err, host.ErrNoTerminal)
}

func TestTheProxyRefusesAHostThatIsNotAllowed(t *testing.T) {
	// arrange
	listener := listen(t)
	var refused []string

	var mu sync.Mutex

	stop := host.ServeProxy(context.Background(), listener, proxy.Options{
		Allow:     func(string, string) bool { return false },
		OnRefused: func(target string) { mu.Lock(); refused = append(refused, target); mu.Unlock() },
	}, io.Discard)

	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	// act
	_, err = io.WriteString(conn, "CONNECT evil.example:443 HTTP/1.1\r\nHost: evil.example:443\r\n\r\n")
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	_ = response.Body.Close()
	stop()

	// assert
	assert.Equal(t, http.StatusForbidden, response.StatusCode)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"evil.example:443"}, refused)
}

func TestAProxyWhoseVMWentAwayIsNotReportedAsBroken(t *testing.T) {
	// arrange
	listener := listen(t)
	stderr := &syncBuffer{}
	require.NoError(t, listener.Close())

	// act
	stop := host.ServeProxy(context.Background(), listener, proxy.Options{}, stderr)
	stop()

	// assert
	assert.Empty(t, stderr.String())
}
