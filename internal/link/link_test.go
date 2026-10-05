package link_test

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/link"
)

// pair is two connected sockets. Unlike net.Pipe they buffer, so that one
// end can write its greeting before the other reads it.
func pair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	require.NoError(t, err)

	conns := make([]net.Conn, 2)

	for i, fd := range fds {
		file := os.NewFile(uintptr(fd), "pair")
		conns[i], err = net.FileConn(file)
		require.NoError(t, err)
		require.NoError(t, file.Close())
		t.Cleanup(func() { _ = conns[i].Close() })
	}

	return conns[0], conns[1]
}

// ends are the two ends of a link over a pair of sockets.
func ends(t *testing.T) (*link.HostEnd, *link.GuestEnd) {
	t.Helper()

	hostConn, guestConn := pair(t)

	guest, err := link.Guest(guestConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = guest.Close() })

	host, err := link.Host(hostConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	return host, guest
}

// accept accepts a stream from the listener, failing the test after a
// while instead of hanging.
func accept(t *testing.T, listener net.Listener) net.Conn {
	t.Helper()

	type result struct {
		conn net.Conn
		err  error
	}

	accepted := make(chan result, 1)

	go func() {
		conn, err := listener.Accept()
		accepted <- result{conn, err}
	}()

	select {
	case r := <-accepted:
		require.NoError(t, r.err)
		t.Cleanup(func() { _ = r.conn.Close() })

		return r.conn
	case <-time.After(5 * time.Second):
		t.Fatal("no stream was accepted")

		return nil
	}
}

// send writes the message on the conn.
func send(t *testing.T, conn net.Conn, message string) {
	t.Helper()

	_, err := conn.Write([]byte(message))
	require.NoError(t, err)
}

// receive reads as many bytes as the message has.
func receive(t *testing.T, conn net.Conn, size int) string {
	t.Helper()

	buf := make([]byte, size)
	_, err := io.ReadFull(conn, buf)
	require.NoError(t, err)

	return string(buf)
}

func TestATerminalStreamOfTheGuestReachesTheTerminalOfTheHost(t *testing.T) {
	// arrange
	host, guest := ends(t)

	// act
	conn, err := guest.DialTerminal()
	require.NoError(t, err)
	send(t, conn, "hello")
	accepted := accept(t, host.Terminal())

	// assert
	assert.Equal(t, "hello", receive(t, accepted, 5))
}

func TestTheHostCanAnswerOnAStream(t *testing.T) {
	// arrange
	host, guest := ends(t)
	conn, err := guest.DialTerminal()
	require.NoError(t, err)
	send(t, conn, "ping")
	accepted := accept(t, host.Terminal())
	receive(t, accepted, 4)

	// act
	send(t, accepted, "pong")

	// assert
	assert.Equal(t, "pong", receive(t, conn, 4))
}

func TestEachProxyStreamOfTheGuestReachesTheProxyOfTheHost(t *testing.T) {
	// arrange
	host, guest := ends(t)

	// act
	first, err := guest.DialProxy()
	require.NoError(t, err)
	second, err := guest.DialProxy()
	require.NoError(t, err)
	send(t, first, "one")
	send(t, second, "two")

	// assert
	got := []string{receive(t, accept(t, host.Proxy()), 3), receive(t, accept(t, host.Proxy()), 3)}
	assert.ElementsMatch(t, []string{"one", "two"}, got)
}

func TestAStreamReachesOnlyTheListenerOfItsKind(t *testing.T) {
	// arrange
	host, guest := ends(t)

	// act
	proxy, err := guest.DialProxy()
	require.NoError(t, err)
	send(t, proxy, "proxy")
	terminal, err := guest.DialTerminal()
	require.NoError(t, err)
	send(t, terminal, "terminal")

	// assert
	assert.Equal(t, "terminal", receive(t, accept(t, host.Terminal()), 8))
	assert.Equal(t, "proxy", receive(t, accept(t, host.Proxy()), 5))
}

func TestAStreamOfAnUnknownKindIsTurnedAway(t *testing.T) {
	// arrange
	hostConn, guestConn := pair(t)
	session := rawGuest(t, guestConn)
	host, err := link.Host(hostConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	stream, err := session.Open()
	require.NoError(t, err)

	// act
	_, err = stream.Write([]byte{'x'})
	require.NoError(t, err)
	require.NoError(t, stream.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = stream.Read(make([]byte, 1))

	// assert
	assert.ErrorIs(t, err, io.EOF)
}

func TestClosingOneListenerLeavesTheOtherServing(t *testing.T) {
	// arrange
	host, guest := ends(t)
	require.NoError(t, host.Terminal().Close())

	// act
	conn, err := guest.DialProxy()
	require.NoError(t, err)
	send(t, conn, "still")

	// assert
	assert.Equal(t, "still", receive(t, accept(t, host.Proxy()), 5))
}

func TestAcceptOnAClosedListenerFails(t *testing.T) {
	// arrange
	host, _ := ends(t)
	listener := host.Terminal()
	require.NoError(t, listener.Close())

	// act
	_, err := listener.Accept()

	// assert
	assert.ErrorIs(t, err, net.ErrClosed)
}

func TestAcceptEndsWhenTheLinkGoesAway(t *testing.T) {
	// arrange
	host, guest := ends(t)
	accepted := make(chan error, 1)

	go func() {
		_, err := host.Terminal().Accept()
		accepted <- err
	}()

	// act
	require.NoError(t, guest.Close())

	// assert
	select {
	case err := <-accepted:
		assert.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not end")
	}
}

func TestTheLinkWorksWhenTheHostDialsTheGuest(t *testing.T) {
	// arrange
	// t.TempDir would hold the name of the test, too long for a socket path
	// on macOS
	dir, err := os.MkdirTemp("", "link")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "link.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	guestReady := make(chan *link.GuestEnd, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			guestReady <- nil

			return
		}

		guest, err := link.Guest(conn)
		if err != nil {
			guestReady <- nil

			return
		}

		guestReady <- guest
	}()

	hostConn, err := net.Dial("unix", path)
	require.NoError(t, err)
	host, err := link.Host(hostConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	guest := <-guestReady
	require.NotNil(t, guest)
	t.Cleanup(func() { _ = guest.Close() })

	// act
	conn, err := guest.DialTerminal()
	require.NoError(t, err)
	send(t, conn, "over unix")

	// assert
	assert.Equal(t, "over unix", receive(t, accept(t, host.Terminal()), 9))
}

func TestDialingFailsOnceTheHostIsGone(t *testing.T) {
	// arrange
	host, guest := ends(t)
	require.NoError(t, host.Close())

	// act
	var err error

	deadline := time.Now().Add(5 * time.Second)
	for err == nil && time.Now().Before(deadline) {
		var conn net.Conn

		conn, err = guest.DialProxy()
		if err == nil {
			_ = conn.Close()

			time.Sleep(10 * time.Millisecond)
		}
	}

	// assert
	assert.Error(t, err)
}

// halfCloser is a connection that can stop sending and still receive.
type halfCloser interface {
	CloseWrite() error
}

func TestAStreamThatStopsSendingStillCarriesTheAnswer(t *testing.T) {
	// arrange
	host, guest := ends(t)
	conn, err := guest.DialProxy()
	require.NoError(t, err)
	send(t, conn, "request")
	accepted := accept(t, host.Proxy())

	// act
	closer, ok := conn.(halfCloser)
	require.True(t, ok, "a stream of the guest has no CloseWrite")
	require.NoError(t, closer.CloseWrite())
	request, err := io.ReadAll(accepted)
	require.NoError(t, err)
	send(t, accepted, "answer")

	// assert
	assert.Equal(t, "request", string(request))
	assert.Equal(t, "answer", receive(t, conn, 6))
}

func TestAStreamOfTheHostCanStopSendingToo(t *testing.T) {
	// arrange
	host, guest := ends(t)
	conn, err := guest.DialProxy()
	require.NoError(t, err)
	send(t, conn, "x")
	accepted := accept(t, host.Proxy())
	receive(t, accepted, 1)

	// act
	closer, ok := accepted.(halfCloser)
	require.True(t, ok, "a stream of the host has no CloseWrite")
	send(t, accepted, "done")
	require.NoError(t, closer.CloseWrite())
	got, err := io.ReadAll(conn)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "done", string(got))
}

func isTimeout(err error) bool {
	var timeout net.Error

	return errors.As(err, &timeout) && timeout.Timeout()
}

// readEnds reads from the conn in the background and reports when the read
// ends, with its error.
func readEnds(conn net.Conn) <-chan error {
	ended := make(chan error, 1)

	go func() {
		_, err := conn.Read(make([]byte, 1))
		ended <- err
	}()

	return ended
}

// within waits for the channel, failing the test after a while.
func within(t *testing.T, ended <-chan error, what string) error {
	t.Helper()

	select {
	case err := <-ended:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal(what)

		return nil
	}
}

func TestClosingAStreamOnTheHostEndsAReadThatIsWaiting(t *testing.T) {
	// arrange
	host, guest := ends(t)
	conn, err := guest.DialTerminal()
	require.NoError(t, err)
	send(t, conn, "x")
	accepted := accept(t, host.Terminal())
	receive(t, accepted, 1)
	reading := readEnds(accepted)

	// act
	require.NoError(t, accepted.Close())

	// assert
	assert.Error(t, within(t, reading, "the read on the host went on after Close"))
}

func TestClosingAStreamOnTheGuestEndsAReadThatIsWaiting(t *testing.T) {
	// arrange
	host, guest := ends(t)
	conn, err := guest.DialTerminal()
	require.NoError(t, err)
	send(t, conn, "x")
	accept(t, host.Terminal())
	reading := readEnds(conn)

	// act
	require.NoError(t, conn.Close())

	// assert
	assert.Error(t, within(t, reading, "the read in the guest went on after Close"))
}

// rawGuest is a guest that opens streams without the link, as a guest that
// does not keep to it would.
func rawGuest(t *testing.T, conn net.Conn) *yamux.Session {
	t.Helper()

	config := yamux.DefaultConfig()
	config.AcceptBacklog = 1000
	config.LogOutput = io.Discard

	_, err := io.WriteString(conn, link.Greeting)
	require.NoError(t, err)

	session, err := yamux.Client(conn, config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	return session
}

func TestAStreamThatNeverNamesItsKindIsClosed(t *testing.T) {
	// arrange
	hostConn, guestConn := pair(t)
	guest := rawGuest(t, guestConn)
	host, err := link.HostWithTimeouts(hostConn, 50*time.Millisecond, time.Minute)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	stream, err := guest.OpenStream()
	require.NoError(t, err)

	// act
	reading := readEnds(stream)

	// assert
	assert.ErrorIs(t, within(t, reading, "a stream without a kind stayed open"), io.EOF)
}

func TestAStreamNobodyAcceptsIsClosedAfterAWhile(t *testing.T) {
	// arrange
	hostConn, guestConn := pair(t)
	guest, err := link.Guest(guestConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = guest.Close() })

	host, err := link.HostWithTimeouts(hostConn, time.Minute, 50*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	conn, err := guest.DialTerminal()
	require.NoError(t, err)

	// act
	reading := readEnds(conn)

	// assert
	assert.ErrorIs(t, within(t, reading, "a stream nobody accepted stayed open"), io.EOF)
}

func TestTheGuestCannotMakeTheHostHoldManyStreams(t *testing.T) {
	// arrange
	hostConn, guestConn := pair(t)
	guest := rawGuest(t, guestConn)
	host, err := link.HostWithTimeouts(hostConn, time.Minute, time.Minute)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	// act
	streams := make([]*yamux.Stream, 0, 300)

	for range 300 {
		stream, err := guest.OpenStream()
		require.NoError(t, err)

		_, _ = stream.Write([]byte{'t'})
		streams = append(streams, stream)
	}

	// assert
	held := 0
	for range 20 {
		time.Sleep(50 * time.Millisecond)

		held = host.Streams()
	}

	// a stream the host turned away was reset, one it holds just has nothing
	// to read yet
	turnedAway := 0

	for _, stream := range streams {
		require.NoError(t, stream.SetReadDeadline(time.Now().Add(10*time.Millisecond)))

		if _, err := stream.Read(make([]byte, 1)); err != nil && !isTimeout(err) {
			turnedAway++
		}
	}

	assert.LessOrEqual(t, held, 40, "the host holds every stream the guest opened")
	assert.Positive(t, turnedAway, "no stream was turned away")
}

func TestTheHostTellsAConnectionClosedBeforeTheGreetingFromAGuest(t *testing.T) {
	// arrange
	hostConn, guestConn := pair(t)
	require.NoError(t, guestConn.Close())

	// act
	_, err := link.Host(hostConn)

	// assert
	assert.ErrorIs(t, err, link.ErrNoGuest)
}

func TestTheHostRefusesAConnectionThatIsNoGuest(t *testing.T) {
	// arrange
	hostConn, guestConn := pair(t)
	_, err := io.WriteString(guestConn, "HTTP/1.1 400 Bad Request\r\n")
	require.NoError(t, err)

	// act
	_, err = link.Host(hostConn)

	// assert
	assert.ErrorIs(t, err, link.ErrNotAGuest)
	assert.NotErrorIs(t, err, link.ErrNoGuest)
}
