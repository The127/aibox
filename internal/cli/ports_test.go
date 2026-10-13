package cli

import (
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/vm"
)

// freePort is a port on the loopback nothing listens on right now.
func freePort(t *testing.T) uint16 {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = listener.Close() }()

	return uint16(listener.Addr().(*net.TCPAddr).Port) //nolint:gosec // a TCP port fits
}

func TestListenPortsListensOnTheLoopbackForEachPort(t *testing.T) {
	// arrange
	host := freePort(t)

	// act
	forwards, err := listenPorts([]config.Port{{Host: host, VM: 5173}}, nil)

	// assert
	require.NoError(t, err)

	defer closeForwards(forwards)

	require.Len(t, forwards, 1)
	assert.Equal(t, uint16(5173), forwards[0].Port)
	assert.Equal(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(host))), forwards[0].Listener.Addr().String())
}

func TestListenPortsRefusesAPortTheVMNeeds(t *testing.T) {
	for _, port := range []uint16{vmProxyPort, forwarderPort, brokerPort, idePort, vm.ClipboardPort} {
		// act
		_, err := listenPorts([]config.Port{{Host: freePort(t), VM: port}}, nil)

		// assert
		require.ErrorIs(t, err, errVMPortTaken, port)
	}
}

func TestListenPortsRefusesAPortOfTheAllowList(t *testing.T) {
	// act
	_, err := listenPorts([]config.Port{{Host: freePort(t), VM: 8123}}, []uint16{8123})

	// assert
	require.ErrorIs(t, err, errVMPortAllowed)
}

func TestListenPortsFailsOnATakenPortAndClosesTheOthers(t *testing.T) {
	// arrange
	free := freePort(t)

	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = taken.Close() }()

	takenPort := uint16(taken.Addr().(*net.TCPAddr).Port) //nolint:gosec // a TCP port fits

	// act
	_, err = listenPorts([]config.Port{{Host: free, VM: 5173}, {Host: takenPort, VM: 3000}}, nil)

	// assert
	require.ErrorContains(t, err, "port "+strconv.Itoa(int(takenPort))+" of this machine")

	again, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(free))))
	require.NoError(t, err, "the first port stayed open")
	_ = again.Close()
}
