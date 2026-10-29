package host_test

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/session"
)

// guestThen connects to the listener and serves a session on the terminal
// that prints first and exits with the code, then a session without a
// terminal that prints its results and its progress.
func guestThen(t *testing.T, listener net.Listener, first string, code int) <-chan error {
	t.Helper()

	served := make(chan error, 1)

	go func() {
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			served <- err

			return
		}

		defer func() { _ = conn.Close() }()

		served <- session.Serve(conn,
			func(session.Request) (session.Process, error) {
				return &process{output: strings.NewReader(first), code: code}, nil
			},
			func(session.Request) (session.Process, error) {
				return &pipedProcess{process: process{output: strings.NewReader("results")}, stderr: strings.NewReader("progress")}, nil
			},
		)
	}()

	return served
}

func TestThenRunsAfterTheSessionOnTheTerminal(t *testing.T) {
	// arrange
	listener := listen(t)
	screen, results, progress := &syncBuffer{}, &syncBuffer{}, &syncBuffer{}
	end := host.ServeTerminal(context.Background(), listener, host.Session{
		Stdout:   screen,
		EndDelay: 5 * time.Second,
		Then:     &backend.Then{Stdout: results, Stderr: progress, Timeout: 5 * time.Second},
	})
	served := guestThen(t, listener, "on the terminal", 3)

	// act
	require.NoError(t, <-served)
	ended := end()

	// assert
	assert.Equal(t, &backend.ExitError{Code: 3}, host.Result(nil, ended, ""))
	assert.Contains(t, screen.String(), "on the terminal")
	assert.Equal(t, "results", results.String())
	assert.Equal(t, "progress", progress.String())
}

func TestThenFailsWhenTheVMServesNoSecondSession(t *testing.T) {
	// arrange
	listener := listen(t)
	end := host.ServeTerminal(context.Background(), listener, host.Session{
		Stdout:   &syncBuffer{},
		EndDelay: 5 * time.Second,
		Then:     &backend.Then{Stdout: &syncBuffer{}, Stderr: &syncBuffer{}, Timeout: 5 * time.Second},
	})
	served := guest(t, listener, "on the terminal", 0)

	// act
	require.NoError(t, <-served)
	ended := end()

	// assert
	assert.ErrorContains(t, host.Result(nil, ended, ""), "open the session")
}

func TestThenIsStoppedAfterItsTimeout(t *testing.T) {
	// arrange
	listener := listen(t)
	end := host.ServeTerminal(context.Background(), listener, host.Session{
		Stdout:   &syncBuffer{},
		EndDelay: 5 * time.Second,
		Then:     &backend.Then{Stdout: &syncBuffer{}, Stderr: &syncBuffer{}, Timeout: 100 * time.Millisecond},
	})

	never, _ := io.Pipe()
	served := make(chan struct{})

	go func() {
		defer close(served)

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		_ = session.Serve(conn,
			func(session.Request) (session.Process, error) {
				return &process{output: strings.NewReader("")}, nil
			},
			func(session.Request) (session.Process, error) {
				return &pipedProcess{process: process{output: never}, stderr: strings.NewReader("")}, nil
			},
		)
	}()

	// act
	<-served
	ended := end()

	// assert
	assert.ErrorContains(t, host.Result(nil, ended, ""), "took longer than 100ms")
}

func TestTheForwardedPortsCloseBeforeThen(t *testing.T) {
	// arrange
	listener, forwarded := listen(t), listen(t)
	thenStarted := make(chan struct{})
	end := host.ServeTerminal(context.Background(), listener, host.Session{
		Stdout:   &syncBuffer{},
		EndDelay: 5 * time.Second,
		Forwards: []backend.Forward{{Listener: forwarded, Port: 1}},
		Then:     &backend.Then{Stdout: &syncBuffer{}, Stderr: &syncBuffer{}, Timeout: 5 * time.Second},
	})

	release := make(chan struct{})
	output, prints := io.Pipe()

	go func() {
		<-release
		_ = prints.Close()
	}()

	served := make(chan error, 1)

	go func() {
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			served <- err

			return
		}

		defer func() { _ = conn.Close() }()

		served <- session.Serve(conn,
			func(session.Request) (session.Process, error) {
				return &process{output: strings.NewReader("")}, nil
			},
			func(session.Request) (session.Process, error) {
				close(thenStarted)

				return &pipedProcess{process: process{output: output}, stderr: strings.NewReader("")}, nil
			},
		)
	}()

	<-thenStarted

	// act
	_, err := forwarded.Accept()

	// assert
	assert.ErrorIs(t, err, net.ErrClosed)

	close(release)
	require.NoError(t, <-served)
	end()
}
