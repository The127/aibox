package hvservice_test

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"os/user"
	"testing"

	winio "github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hvservice"
)

// pipeFromClientAt is the server end of a pipe whose client, this test as
// well, has sent a request, as the service would have one. The client
// lets the server know it at the level.
func pipeFromClientAt(t *testing.T, level winio.PipeImpLevel) windows.Handle {
	t.Helper()

	name := make([]byte, 8)
	_, _ = rand.Read(name)
	path := `\\.\pipe\aibox-test-` + hex.EncodeToString(name)

	l, err := winio.ListenPipe(path, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	type dial struct {
		conn net.Conn
		err  error
	}

	// the dial waits for the server to accept; a dial that fails closes
	// the listener, so that the server doesn't wait for it in turn
	dialed := make(chan dial, 1)

	go func() {
		c, err := winio.DialPipeAccessImpLevel(t.Context(), path, windows.GENERIC_READ|windows.GENERIC_WRITE, level)
		if err != nil {
			_ = l.Close()
		}

		dialed <- dial{c, err}
	}()

	server, acceptErr := l.Accept()
	client := <-dialed
	require.NoError(t, client.err)
	t.Cleanup(func() { _ = client.conn.Close() })
	require.NoError(t, acceptErr)
	t.Cleanup(func() { _ = server.Close() })

	// the write waits for the server to read; a write that fails closes the
	// client, so that the read fails rather than waits
	go func() {
		if err := hvservice.Write(client.conn, hvservice.Request{Op: hvservice.OpStart}); err != nil {
			_ = client.conn.Close()
		}
	}()

	var request hvservice.Request
	require.NoError(t, hvservice.Read(hvservice.NewReader(server), &request))

	return windows.Handle(server.(interface{ Fd() uintptr }).Fd())
}

func TestClientOfTellsWhoTheClientIsFromItsToken(t *testing.T) {
	t.Parallel()

	for name, level := range map[string]winio.PipeImpLevel{
		"identify":    winio.PipeImpLevelIdentification,
		"impersonate": winio.PipeImpLevelImpersonation,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			me, err := user.Current()
			require.NoError(t, err)

			// act
			client, err := hvservice.ClientOf(pipeFromClientAt(t, level))

			// assert
			require.NoError(t, err)
			assert.Equal(t, hvservice.Client{User: me.Uid}, client)
			assert.Equal(t, []string{me.Uid}, client.SIDs())
		})
	}
}

func TestClientOfRefusesAClientThatHidesWhoItIs(t *testing.T) {
	t.Parallel()

	// act
	_, err := hvservice.ClientOf(pipeFromClientAt(t, winio.PipeImpLevelAnonymous))

	// assert
	assert.ErrorIs(t, err, hvservice.ErrAnonymous)
}
