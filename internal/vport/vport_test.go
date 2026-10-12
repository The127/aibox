package vport_test

import (
	"encoding/binary"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/vport"
)

const (
	proxy    uint32 = 1
	terminal uint32 = 2
	timeout         = 5 * time.Second
)

// connect joins the side of the VM and the side of the host over a pipe
// that stands for the port.
func connect(t *testing.T) (*vport.Dialer, []net.Listener) {
	t.Helper()

	vmSide, hostSide := net.Pipe()

	dialer, err := vport.NewDialer(vmSide)
	require.NoError(t, err)

	listeners, err := vport.Listen(hostSide, proxy, terminal)
	require.NoError(t, err)

	t.Cleanup(func() {
		for _, l := range listeners {
			_ = l.Close()
		}

		_ = vmSide.Close()
	})

	return dialer, listeners
}

// accept accepts one connection on the listener in the background.
func accept(l net.Listener) <-chan net.Conn {
	accepted := make(chan net.Conn, 1)

	go func() {
		conn, err := l.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	return accepted
}

func received(t *testing.T, accepted <-chan net.Conn) net.Conn {
	t.Helper()

	select {
	case conn := <-accepted:
		return conn
	case <-time.After(timeout):
		t.Fatal("no connection arrived")

		return nil
	}
}

func TestDialReachesTheListenerOfTheService(t *testing.T) {
	// arrange
	dialer, listeners := connect(t)
	toProxy, toTerminal := accept(listeners[0]), accept(listeners[1])

	// act
	conn, err := dialer.Dial(terminal)
	require.NoError(t, err)

	_, err = conn.Write([]byte("hello"))
	require.NoError(t, err)

	// assert
	host := received(t, toTerminal)
	got := make([]byte, 5)
	_, err = io.ReadFull(host, got)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))

	_, err = host.Write([]byte("back"))
	require.NoError(t, err)

	back := make([]byte, 4)
	_, err = io.ReadFull(conn, back)
	require.NoError(t, err)
	assert.Equal(t, "back", string(back))

	select {
	case <-toProxy:
		t.Fatal("the proxy got the connection of the terminal")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDialOfAnUnknownServiceGetsClosed(t *testing.T) {
	// arrange
	dialer, _ := connect(t)

	// act
	conn, err := dialer.Dial(99)
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(timeout)))

	_, err = conn.Read(make([]byte, 1))

	// assert
	assert.ErrorIs(t, err, io.EOF)
}

func TestAStreamThatNeverSaysItsServiceHoldsUpNoOther(t *testing.T) {
	// arrange
	vmSide, hostSide := net.Pipe()
	listeners, err := vport.Listen(hostSide, proxy)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listeners[0].Close() })

	session, err := yamux.Client(vmSide, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	toProxy := accept(listeners[0])

	// act
	silent, err := session.OpenStream()
	require.NoError(t, err)

	_, err = silent.Write([]byte{0})
	require.NoError(t, err)

	speaking, err := session.OpenStream()
	require.NoError(t, err)
	require.NoError(t, binary.Write(speaking, binary.BigEndian, proxy))

	// assert
	received(t, toProxy)
}

func TestClosingEveryListenerClosesThePort(t *testing.T) {
	// arrange
	vmSide, hostSide := net.Pipe()
	listeners, err := vport.Listen(hostSide, proxy, terminal)
	require.NoError(t, err)

	// act
	require.NoError(t, listeners[0].Close())
	require.NoError(t, listeners[1].Close())

	// assert
	session, err := yamux.Client(vmSide, nil)
	require.NoError(t, err)

	select {
	case <-session.CloseChan():
	case <-time.After(timeout):
		t.Fatal("the port stayed open")
	}
}

func TestAcceptEndsWhenThePortCloses(t *testing.T) {
	// arrange
	vmSide, hostSide := net.Pipe()
	listeners, err := vport.Listen(hostSide, proxy)
	require.NoError(t, err)

	// act
	require.NoError(t, vmSide.Close())

	// assert
	_, err = listeners[0].Accept()
	assert.ErrorIs(t, err, net.ErrClosed)
}

func TestFindReturnsTheDeviceOfThePortWithTheName(t *testing.T) {
	// arrange
	sys := t.TempDir()
	ports := filepath.Join(sys, "class", "virtio-ports")
	require.NoError(t, os.MkdirAll(filepath.Join(ports, "vport0p0"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(ports, "vport0p1"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(ports, "vport0p0", "name"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(ports, "vport0p1", "name"), []byte("aibox\n"), 0o600))

	// act
	device, err := vport.Find(sys, "/dev", "aibox")

	// assert
	require.NoError(t, err)
	assert.Equal(t, "/dev/vport0p1", device)
}

func TestFindSkipsAPortWhoseNameItCannotRead(t *testing.T) {
	// arrange
	sys := t.TempDir()
	ports := filepath.Join(sys, "class", "virtio-ports")
	require.NoError(t, os.MkdirAll(filepath.Join(ports, "vport0p0", "name"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(ports, "vport0p1"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(ports, "vport0p1", "name"), []byte("aibox\n"), 0o600))

	// act
	device, err := vport.Find(sys, "/dev", "aibox")

	// assert
	require.NoError(t, err)
	assert.Equal(t, "/dev/vport0p1", device)
}

func TestFindWithoutThePortIsNotExist(t *testing.T) {
	// arrange
	sys := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(sys, "class", "virtio-ports", "vport0p0"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sys, "class", "virtio-ports", "vport0p0", "name"), nil, 0o600))

	// act
	_, err := vport.Find(sys, "/dev", "aibox")

	// assert
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestFindWithoutVirtioPortsIsNotExist(t *testing.T) {
	// act
	_, err := vport.Find(t.TempDir(), "/dev", "aibox")

	// assert
	assert.ErrorIs(t, err, fs.ErrNotExist)
}
