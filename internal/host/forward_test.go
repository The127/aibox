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

// openProcess is a command in the VM that runs until exit is closed.
type openProcess struct {
	exit chan struct{}
}

func (p *openProcess) Read([]byte) (int, error) {
	<-p.exit

	return 0, io.EOF
}

func (p *openProcess) Write(b []byte) (int, error) { return len(b), nil }
func (p *openProcess) Resize(session.Size) error   { return nil }
func (p *openProcess) Close() error                { return nil }
func (p *openProcess) Wait() (int, error)          { <-p.exit; return 0, nil }

func TestAConnectionToAForwardedPortReachesTheServerInTheVM(t *testing.T) {
	// arrange
	server := listen(t)

	go func() {
		conn, err := server.Accept()
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		_, _ = io.WriteString(conn, "hello from the server in the VM")
	}()

	forwarded := listen(t)
	terminal := listen(t)
	process := &openProcess{exit: make(chan struct{})}
	end := host.ServeTerminal(context.Background(), terminal, host.Session{
		Stdout:   io.Discard,
		EndDelay: 5 * time.Second,
		Forwards: []backend.Forward{{Listener: forwarded, Port: uint16(server.Addr().(*net.TCPAddr).Port)}}, //nolint:gosec // a TCP port fits
	})

	served := make(chan error, 1)

	go func() {
		conn, err := net.Dial("tcp", terminal.Addr().String())
		if err != nil {
			served <- err

			return
		}

		defer func() { _ = conn.Close() }()

		served <- session.Serve(conn, func(session.Request) (session.Process, error) { return process, nil })
	}()

	// act
	conn, err := net.Dial("tcp", forwarded.Addr().String())
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	answer, err := io.ReadAll(conn)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "hello from the server in the VM", string(answer))

	close(process.exit)
	require.NoError(t, <-served)
	end()

	_, err = forwarded.Accept()
	assert.True(t, err != nil && strings.Contains(err.Error(), "closed"), "the listener is closed once the session ends")
}
