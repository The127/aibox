package hvservice_test

import (
	"io"
	"testing"

	winio "github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

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

func TestConsoleSDDLLetsSystemAndTheVMsInAndNoOneElse(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "D:P(A;;GA;;;SY)(A;;0x12019B;;;S-1-5-83-0)", hvservice.ConsoleSDDL)
}

// aConsole is a console pipe the test may connect to, as Hyper-V would,
// in the hands of a Console, as aibox holds it.
func aConsole(t *testing.T) (name string, console *hvservice.Console) {
	t.Helper()

	name = pipePath()

	h, err := hvservice.MakeConsole(name, "D:P(A;;GA;;;"+me(t)+")")
	require.NoError(t, err)

	console = hvservice.ConsoleOf(uint64(h))
	t.Cleanup(func() { _ = console.Close() })

	require.NoError(t, console.Listen())

	return name, console
}

func TestTheConsoleIsWhatTheVMWritesToThePipe(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{"the VM connects first": true, "aibox waits first": false}

	for name, vmFirst := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			name, console := aConsole(t)

			connected := make(chan struct{})
			if !vmFirst {
				close(connected)
			}

			written := make(chan error, 1)

			go func() {
				<-connected

				hyperV, err := winio.DialPipe(name, nil)
				if err != nil {
					written <- err

					return
				}

				_, err = hyperV.Write([]byte("[    0.000000] Linux\n"))
				written <- err

				_ = hyperV.Close()
			}()

			if vmFirst {
				close(connected)
				require.NoError(t, <-written)
			}

			// act
			r, err := console.Connect()
			require.NoError(t, err)

			b, err := io.ReadAll(r)

			// assert
			require.NoError(t, err)

			if !vmFirst {
				require.NoError(t, <-written)
			}

			assert.Equal(t, "[    0.000000] Linux\n", string(b))
		})
	}
}

func TestClosingTheConsoleEndsTheWaitForTheVM(t *testing.T) {
	t.Parallel()

	// arrange
	_, console := aConsole(t)

	waited := make(chan error, 1)

	go func() {
		_, err := console.Connect()
		waited <- err
	}()

	// act
	require.NoError(t, console.Close())

	// assert
	assert.Error(t, <-waited)
}

func TestTheConsolePipeHasOneInstanceNoOneCanAddTo(t *testing.T) {
	t.Parallel()

	// arrange
	name, _ := aConsole(t)

	// act: as the user who may do all with the pipe
	_, err := hvservice.MakeConsole(name, "D:P(A;;GA;;;"+me(t)+")")

	// assert
	assert.ErrorIs(t, err, windows.ERROR_PIPE_BUSY)
}
