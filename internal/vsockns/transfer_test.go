//go:build linux

package vsockns

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func socketPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)

	sender, receiver := os.NewFile(uintptr(pair[0]), "sender"), os.NewFile(uintptr(pair[1]), "receiver")
	t.Cleanup(func() { _ = sender.Close(); _ = receiver.Close() })

	return sender, receiver
}

func devNulls(t *testing.T, count int) []*os.File {
	t.Helper()

	files := make([]*os.File, count)

	for i := range files {
		file, err := os.Open(os.DevNull)
		require.NoError(t, err)
		t.Cleanup(func() { _ = file.Close() })

		files[i] = file
	}

	return files
}

func TestReceiveReturnsTheFilesAndPortsSent(t *testing.T) {
	// arrange
	sender, receiver := socketPair(t)
	require.NoError(t, send(sender, devNulls(t, 3), [2]uint32{4321, 5432}))

	// act
	files, ports, err := receive(receiver)

	// assert
	require.NoError(t, err)
	assert.Equal(t, [2]uint32{4321, 5432}, ports)
	require.Len(t, files, 3)

	for _, file := range files {
		info, statErr := file.Stat()
		require.NoError(t, statErr)
		assert.NotZero(t, info.Mode()&os.ModeCharDevice)
		_ = file.Close()
	}
}

func TestReceiveRejectsTheWrongNumberOfFiles(t *testing.T) {
	// arrange
	sender, receiver := socketPair(t)
	require.NoError(t, send(sender, devNulls(t, 2), [2]uint32{1, 2}))

	// act
	files, _, err := receive(receiver)

	// assert
	require.ErrorContains(t, err, "2 files")
	assert.Nil(t, files)
}

func TestReceiveRejectsAMessageWithoutFiles(t *testing.T) {
	// arrange
	sender, receiver := socketPair(t)
	_, err := sender.Write([]byte("12345678"))
	require.NoError(t, err)

	// act
	_, _, err = receive(receiver)

	// assert
	assert.ErrorContains(t, err, "0 files")
}

func TestReceiveReportsAClosedSocket(t *testing.T) {
	// arrange
	sender, receiver := socketPair(t)
	require.NoError(t, sender.Close())

	// act
	_, _, err := receive(receiver)

	// assert
	assert.ErrorContains(t, err, "0 bytes")
}
