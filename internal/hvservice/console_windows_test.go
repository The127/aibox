package hvservice_test

import (
	"testing"

	winio "github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hvservice"
)

func TestConsolePipeIsNamedAfterTheVM(t *testing.T) {
	t.Parallel()

	// arrange
	id, err := guid.FromString("5c2ee6b0-5a8f-4b4a-9c55-0c2b5f7e8f10")
	require.NoError(t, err)

	// act
	pipe := hvservice.ConsolePipe(id)

	// assert
	assert.Equal(t, `\\.\pipe\aibox-console-5c2ee6b0-5a8f-4b4a-9c55-0c2b5f7e8f10`, pipe)
}

func TestConsoleSDDLLetsTheUserSystemAndTheVMsInAndNoOneElse(t *testing.T) {
	t.Parallel()

	// act
	sddl, err := hvservice.ConsoleSDDL("S-1-5-21-1-2-3-1001")

	// assert
	require.NoError(t, err)
	assert.Equal(t, "D:P(A;;GA;;;SY)(A;;GA;;;S-1-5-21-1-2-3-1001)(A;;0x12019B;;;S-1-5-83-0)", sddl)
}

func TestConsoleSDDLRefusesWhatIsNoSID(t *testing.T) {
	t.Parallel()

	// act
	_, err := hvservice.ConsoleSDDL("WD")

	// assert
	assert.ErrorIs(t, err, hvservice.ErrBadSID)
}

// The security lets the user, who makes the pipe, make its instances, as
// a listener does at each connection.
func TestTheUserCanMakeAndServeTheConsolePipe(t *testing.T) {
	t.Parallel()

	// arrange
	sddl, err := hvservice.ConsoleSDDL(me(t))
	require.NoError(t, err)

	path := pipePath()

	// act
	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: sddl})
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	accepted := make(chan error, 1)

	go func() {
		c, err := l.Accept()
		if err == nil {
			_ = c.Close()
		}

		accepted <- err
	}()

	c, err := winio.DialPipe(path, nil)

	// assert
	require.NoError(t, err)
	_ = c.Close()
	assert.NoError(t, <-accepted)
}
