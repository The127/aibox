package hvsock

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAClosedListenerSaysSoAsTheProxyAndTheTerminalExpect(t *testing.T) {
	t.Parallel()

	// arrange
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	l := Closing(inner)

	accepted := make(chan error, 1)

	go func() {
		_, err := l.Accept()
		accepted <- err
	}()

	// act
	require.NoError(t, l.Close())

	// assert
	assert.ErrorIs(t, <-accepted, net.ErrClosed, "an Accept that waited")

	_, err = l.Accept()
	assert.ErrorIs(t, err, net.ErrClosed, "an Accept after the close")
}
