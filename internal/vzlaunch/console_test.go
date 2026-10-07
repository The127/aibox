//go:build darwin && cgo

package vzlaunch

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedBuffer is a buffer the copy of the console may write to while the
// test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.String()
}

func TestTheConsoleLogGetsAllTheConsoleWroteBeforeTheEnd(t *testing.T) {
	// arrange
	var log lockedBuffer

	in, finish, err := pipeConsole(&log, time.Minute)
	require.NoError(t, err)

	console := strings.Repeat("a line of the console\n", 1<<15)

	_, err = in.WriteString(console)
	require.NoError(t, err)

	// act
	finish()

	// assert
	assert.Equal(t, console, log.String())
}

func TestTheConsoleLogEndsWhenTheVMHoldsOnToThePipe(t *testing.T) {
	// arrange
	var log lockedBuffer

	in, finish, err := pipeConsole(&log, 100*time.Millisecond)
	require.NoError(t, err)

	// the VM keeps a copy of the end of the pipe open
	fd, err := syscall.Dup(int(in.Fd()))
	require.NoError(t, err)

	vm := os.NewFile(uintptr(fd), "vm")
	t.Cleanup(func() { _ = vm.Close() })

	_, err = in.WriteString("last words\n")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return log.String() == "last words\n" }, 10*time.Second, time.Millisecond)

	// act
	started := time.Now()
	done := make(chan struct{})

	go func() {
		defer close(done)

		finish()
	}()

	// assert
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the end of the console log waits for the VM")
	}

	assert.GreaterOrEqual(t, time.Since(started), 100*time.Millisecond, "the console log ends before the VM had its moment")
}
