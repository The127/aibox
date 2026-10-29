package session_test

import (
	"context"
	"crypto/ed25519"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/the127/aibox/internal/session"
)

// echoServer is a server on the loopback that answers each connection with
// everything it read, and returns its port.
func echoServer(t *testing.T) uint16 {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				_, _ = io.Copy(conn, conn)
				_ = conn.(*net.TCPConn).CloseWrite()
			}()
		}
	}()

	return uint16(listener.Addr().(*net.TCPAddr).Port) //nolint:gosec // a TCP port fits
}

func TestAConnectionReachesAPortOnTheLoopbackOfTheVM(t *testing.T) {
	// arrange
	port := echoServer(t)
	dials := make(chan session.Dial, 1)
	ended := make(chan struct{})

	f := newFixture(t, nil, func(s *setup) {
		s.forward = func(ctx context.Context, dial session.Dial) {
			dials <- dial
			<-ctx.Done()
			close(ended)
		}
	})
	f.waitStarted(t)
	dial := <-dials

	// act
	conn, err := dial(port)
	require.NoError(t, err)

	_, err = io.WriteString(conn, "ping")
	require.NoError(t, err)
	require.NoError(t, conn.(interface{ CloseWrite() error }).CloseWrite())

	answer, err := io.ReadAll(conn)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "ping", string(answer))

	f.exited(t, 0)
	<-f.attached
	<-ended
}

func TestTheVMRefusesAConnectionOffItsLoopback(t *testing.T) {
	// arrange
	port := strconv.Itoa(int(echoServer(t)))
	guestSide, hostSide := connPair(t)

	go func() { _ = session.Serve(guestSide, nil) }()

	conn, channels, requests, err := ssh.NewClientConn(hostSide, "aibox", &ssh.ClientConfig{
		User:            "user",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // a test
	})
	require.NoError(t, err)

	client := ssh.NewClient(conn, channels, requests)
	defer func() { _ = client.Close() }()

	for _, address := range []string{"localhost:" + port, "[::1]:" + port, "0.0.0.0:" + port} {
		// act
		_, err := client.Dial("tcp", address)

		// assert
		assert.Error(t, err, address)
	}
}

// silentServer serves a session whose command exits once exit is closed.
// It never answers a connection to a port, and closes asked when the first
// one arrives.
func silentServer(t *testing.T, conn net.Conn, asked chan<- struct{}, exit <-chan struct{}) {
	t.Helper()

	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	require.NoError(t, err)

	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	server, channels, requests, err := ssh.NewServerConn(conn, config)
	require.NoError(t, err)

	t.Cleanup(func() { _ = server.Close() })

	go ssh.DiscardRequests(requests)

	go func() {
		for newChannel := range channels {
			if newChannel.ChannelType() != "session" {
				close(asked)

				continue
			}

			channel, channelRequests, err := newChannel.Accept()
			if err != nil {
				return
			}

			go func() {
				for request := range channelRequests {
					_ = request.Reply(true, nil)

					if request.Type == "shell" {
						go func() {
							<-exit
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							_ = channel.Close()
						}()
					}
				}
			}()
		}
	}()
}

func TestASessionEndsWhileTheVMLeavesAConnectionUnanswered(t *testing.T) {
	// arrange
	guestSide, hostSide := connPair(t)
	asked := make(chan struct{})
	exit := make(chan struct{})

	forward := func(_ context.Context, dial session.Dial) {
		if conn, err := dial(5173); err == nil {
			_ = conn.Close()
		}
	}

	attached := make(chan error, 1)

	go func() {
		_, err := attach(hostSide, session.Client{}, forward)
		attached <- err
	}()

	silentServer(t, guestSide, asked, exit)

	select {
	case <-asked:
	case <-time.After(patience):
		t.Fatal("the client did not connect to the port")
	}

	// act
	close(exit)

	// assert
	select {
	case err := <-attached:
		require.NoError(t, err)
	case <-time.After(patience):
		t.Fatal("the session did not end")
	}
}
