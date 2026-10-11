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

// pipePath is the path of a pipe of a test's own.
func pipePath() string {
	name := make([]byte, 8)
	_, _ = rand.Read(name)

	return `\\.\pipe\aibox-test-` + hex.EncodeToString(name)
}

// me is the SID of whoever runs the test.
func me(t *testing.T) string {
	t.Helper()

	u, err := user.Current()
	require.NoError(t, err)

	return u.Uid
}

// isMe tells an owner that is whoever runs the test.
func isMe(t *testing.T) func(*windows.SID) bool {
	t.Helper()

	sid := me(t)

	return func(owner *windows.SID) bool { return owner.String() == sid }
}

// listen listens on a pipe with the security descriptor.
func listen(t *testing.T, path, sddl string) net.Listener {
	t.Helper()

	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: sddl})
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	return l
}

func TestDialLetsTheServiceKnowWhoCallsAndNoMore(t *testing.T) {
	t.Parallel()

	// arrange
	path := pipePath()
	l := listen(t, path, ownPipe(t))

	type dialed struct {
		conn net.Conn
		err  error
	}

	done := make(chan dialed, 1)

	// a dial that fails closes the listener, and a write that fails the
	// client, so that the test fails rather than waits
	go func() {
		c, err := hvservice.DialPipe(t.Context(), path, isMe(t))
		if err != nil {
			_ = l.Close()
			done <- dialed{err: err}

			return
		}

		done <- dialed{conn: c}

		if err := hvservice.Write(c, hvservice.Request{Op: hvservice.OpStart}); err != nil {
			_ = c.Close()
		}
	}()

	server, acceptErr := l.Accept()
	d := <-done
	require.NoError(t, d.err)
	t.Cleanup(func() { _ = d.conn.Close() })
	require.NoError(t, acceptErr)
	t.Cleanup(func() { _ = server.Close() })

	var request hvservice.Request
	require.NoError(t, hvservice.Read(hvservice.NewReader(server), &request))

	// act
	client, err := hvservice.ClientOf(windows.Handle(server.(interface{ Fd() uintptr }).Fd()))

	// assert
	require.NoError(t, err)
	assert.Equal(t, me(t), client.User)
}

func TestDialRefusesAPipeTheServiceDidNotMake(t *testing.T) {
	t.Parallel()

	// arrange
	path := pipePath()
	l := listen(t, path, ownPipe(t))

	go func() {
		if c, err := l.Accept(); err == nil {
			_ = c.Close()
		}
	}()

	// act
	_, err := hvservice.DialPipe(t.Context(), path, func(owner *windows.SID) bool {
		return owner.IsWellKnown(windows.WinLocalSystemSid)
	})

	// assert
	assert.ErrorIs(t, err, hvservice.ErrNotService)
}

// secondInstance tries to add an instance to the pipe at path, as someone
// who would answer the service's clients in its place.
func secondInstance(t *testing.T, path string) error {
	t.Helper()

	p, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)

	h, err := windows.CreateNamedPipe(p, windows.PIPE_ACCESS_DUPLEX, windows.PIPE_TYPE_BYTE, windows.PIPE_UNLIMITED_INSTANCES, 0, 0, 0, nil)
	if err == nil {
		_ = windows.CloseHandle(h)
	}

	return err
}

func TestThePipeLetsNoUserAddAnInstanceOfTheirOwn(t *testing.T) {
	t.Parallel()

	// arrange
	path := pipePath()
	listen(t, path, hvservice.PipeSDDLFor(me(t), me(t)))

	// act
	err := secondInstance(t, path)

	// assert
	assert.ErrorIs(t, err, windows.ERROR_ACCESS_DENIED)
}

func TestAPipeOpenToWriteLetsAUserAddAnInstance(t *testing.T) {
	t.Parallel()

	// arrange: a pipe open to write, as GENERIC_WRITE would make it, which
	// TestThePipeLetsNoUserAddAnInstanceOfTheirOwn checks is no longer so
	path := pipePath()
	listen(t, path, "D:P(A;;GRGW;;;"+me(t)+")")

	// act
	err := secondInstance(t, path)

	// assert
	assert.NoError(t, err)
}

func TestThePipeOfTheServiceIsSystemsAndOpenToThePeopleAtTheMachine(t *testing.T) {
	t.Parallel()

	// assert
	assert.Equal(t, "O:SYD:P(A;;GA;;;SY)(A;;0x12019B;;;IU)(A;;0x12019B;;;AC)S:(ML;;NW;;;LW)", hvservice.PipeSDDL)
}

// ownPipe is the security of a pipe whoever runs the test may do all
// with: as the service, it makes the instances its clients connect to.
func ownPipe(t *testing.T) string {
	t.Helper()

	return "O:" + me(t) + "D:P(A;;GA;;;" + me(t) + ")"
}
