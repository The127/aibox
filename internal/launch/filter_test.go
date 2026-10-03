package launch

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/mdlayher/vsock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addressed is a connection with a remote address of the test's choosing.
type addressed struct {
	net.Conn
	remote net.Addr
}

func (c addressed) RemoteAddr() net.Addr { return c.remote }

// queue is a listener that hands out the connections it was given.
type queue struct {
	conns chan net.Conn
}

func (q *queue) Accept() (net.Conn, error) {
	conn, ok := <-q.conns
	if !ok {
		return nil, net.ErrClosed
	}

	return conn, nil
}

func (q *queue) Close() error   { return nil }
func (q *queue) Addr() net.Addr { return &vsock.Addr{} }

func TestForGuestDropsTheConnectionsOfOthers(t *testing.T) {
	// arrange
	ours, ourPeer := net.Pipe()
	otherVM, otherVMPeer := net.Pipe()
	local, localPeer := net.Pipe()

	defer func() { _ = ourPeer.Close() }()

	q := &queue{conns: make(chan net.Conn, 3)}
	q.conns <- addressed{Conn: otherVM, remote: &vsock.Addr{ContextID: 7, Port: 1}}
	q.conns <- local
	q.conns <- addressed{Conn: ours, remote: &vsock.Addr{ContextID: 42, Port: 1}}

	for _, peer := range []net.Conn{otherVMPeer, localPeer} {
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
	}

	// act
	conn, err := forGuest(q, 42).Accept()

	// assert
	require.NoError(t, err)
	assert.Equal(t, uint32(42), conn.RemoteAddr().(*vsock.Addr).ContextID)

	for _, peer := range []net.Conn{otherVMPeer, localPeer} {
		_, err = peer.Read(make([]byte, 1))
		assert.ErrorIs(t, err, io.EOF)
	}
}
