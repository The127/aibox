package hvserver

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hvservice"
)

func TestTheServiceEndsTheVMsOfItsConnectionsWhenItStops(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, nil)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, stop := context.WithCancel(t.Context())
	ran := make(chan error, 1)

	go func() {
		ran <- f.srv.run(ctx, l, func(net.Conn) peer { return f.peer() })
	}()

	conn, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.NoError(t, hvservice.Write(conn, hvservice.Request{Op: hvservice.OpCreate, Create: &hvservice.Create{MemoryMiB: 2048, CPUs: 2, State: 420}}))

	var reply hvservice.Reply
	require.NoError(t, hvservice.Read(hvservice.NewReader(conn), &reply))
	require.Empty(t, reply.Error)

	// act
	stop()

	// assert
	require.NoError(t, <-ran)
	assert.Equal(t, []string{"create", "terminate", "close", "revoke"}, f.calls.all(), "run returned once the VM had ended")
	assert.Zero(t, f.srv.ledger.used)
}
