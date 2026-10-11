package hvserver

import (
	"crypto/rand"
	"encoding/hex"
	"os/user"
	"testing"

	winio "github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hvservice"
)

// ownPipe is the path and the security of a pipe of the test's own,
// owned by whoever runs it, as a pipe of SYSTEM's would be by the service.
func ownPipe(t *testing.T) (path, sddl string) {
	t.Helper()

	name := make([]byte, 8)
	_, _ = rand.Read(name)

	me, err := user.Current()
	require.NoError(t, err)

	return `\\.\pipe\aibox-test-` + hex.EncodeToString(name), "O:" + me.Uid + "D:P(A;;GA;;;" + me.Uid + ")"
}

func TestTheServiceListensOnAPipeOfItsOwn(t *testing.T) {
	t.Parallel()

	// arrange
	path, sddl := ownPipe(t)

	// act
	l, err := listen(path, sddl)

	// assert
	require.NoError(t, err)
	assert.NoError(t, l.Close())
}

func TestTheServiceRefusesAPipeSomeoneMadeBeforeIt(t *testing.T) {
	t.Parallel()

	// arrange
	path, sddl := ownPipe(t)

	squatter, err := listen(path, sddl)
	require.NoError(t, err)
	t.Cleanup(func() { _ = squatter.Close() })

	// act
	_, err = listen(path, sddl)

	// assert
	assert.Error(t, err)
}

func TestTheServiceTellsWhoIsOnTheOtherEndOfItsPipe(t *testing.T) {
	t.Parallel()

	// arrange
	path, sddl := ownPipe(t)

	l, err := listen(path, sddl)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	// a dial that fails closes the listener, so that Accept fails too
	dialed := make(chan error, 1)

	go func() {
		c, err := winio.DialPipeAccessImpLevel(t.Context(), path, windows.GENERIC_READ|windows.GENERIC_WRITE, winio.PipeImpLevelIdentification)
		if err != nil {
			_ = l.Close()
			dialed <- err

			return
		}

		t.Cleanup(func() { _ = c.Close() })
		dialed <- nil

		if err := hvservice.Write(c, hvservice.Request{Op: hvservice.OpStart}); err != nil {
			_ = c.Close()
		}
	}()

	conn, acceptErr := l.Accept()
	require.NoError(t, <-dialed)
	require.NoError(t, acceptErr)
	t.Cleanup(func() { _ = conn.Close() })

	var request hvservice.Request
	require.NoError(t, hvservice.Read(hvservice.NewReader(conn), &request))

	me, err := user.Current()
	require.NoError(t, err)

	// act
	client, err := clientOf(conn)

	// assert
	require.NoError(t, err)
	assert.Equal(t, me.Uid, client.User)
}
