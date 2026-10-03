package launch_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/vm"
)

func machine() vm.Machine {
	return vm.Machine{
		Kernel:    "/images/vmlinuz",
		Rootfs:    "/images/os.ext4",
		MemoryMiB: 512,
		CPUs:      1,
		Shares: []vm.Share{
			{Tag: "project", Dir: "/home/someone/project"},
			{Tag: "home", Dir: "/home/someone/.aibox/home"},
		},
		GuestCID: 42,
	}
}

func TestRunStartsVirtiofsdForEachShareBeforeQEMU(t *testing.T) {
	// arrange
	f := fakes(t)
	var stdout bytes.Buffer
	options := f.options()
	options.Stdout = &stdout

	// act
	err := launch.Run(context.Background(), machine(), options)

	// assert
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "fake qemu ran")
	assert.Empty(t, f.record(t, "qemu-missing-sockets"))
	assert.Contains(t, f.record(t, "virtiofsd-project.sock"), "--shared-dir=/home/someone/project")
	assert.Contains(t, f.record(t, "virtiofsd-home.sock"), "--shared-dir=/home/someone/.aibox/home")
}

func TestRunStopsVirtiofsdAndRemovesTheSockets(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(), f.options())

	// assert
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(f.records, "virtiofsd-project.sock-stopped"))
	assert.FileExists(t, filepath.Join(f.records, "virtiofsd-home.sock-stopped"))

	sockets := f.record(t, "qemu-sockets")
	require.NotEmpty(t, sockets)
	assert.NoDirExists(t, filepath.Dir(sockets[0]))
}

func TestRunWhenQEMUFails(t *testing.T) {
	// arrange
	f := fakes(t)
	t.Setenv("AIBOX_FAKE_QEMU_EXIT", "3")

	// act
	err := launch.Run(context.Background(), machine(), f.options())

	// assert
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 3, exitErr.ExitCode())
}

func TestRunWhenVirtiofsdCreatesNoSocket(t *testing.T) {
	// arrange
	f := fakes(t)
	t.Setenv("AIBOX_FAKE_VIRTIOFSD", "no-socket")
	options := f.options()
	options.SocketTimeout = 100 * time.Millisecond

	// act
	err := launch.Run(context.Background(), machine(), options)

	// assert
	require.ErrorIs(t, err, launch.ErrSocketTimeout)
	assert.NoFileExists(t, filepath.Join(f.records, "qemu"))
	assert.FileExists(t, filepath.Join(f.records, "virtiofsd-project.sock-stopped"))
	assert.NoDirExists(t, socketDir(t, f.record(t, "virtiofsd-project.sock")))
}

func socketDir(t *testing.T, virtiofsdArgs []string) string {
	t.Helper()

	for _, arg := range virtiofsdArgs {
		if socket, ok := strings.CutPrefix(arg, "--socket-path="); ok {
			return filepath.Dir(socket)
		}
	}

	t.Fatal("no --socket-path in", virtiofsdArgs)

	return ""
}

func TestRunWhenVirtiofsdExits(t *testing.T) {
	// arrange
	f := fakes(t)
	t.Setenv("AIBOX_FAKE_VIRTIOFSD", "exit")
	stderr := &syncBuffer{}
	options := f.options()
	options.Stderr = stderr
	options.SocketTimeout = 5 * time.Second
	start := time.Now()

	// act
	err := launch.Run(context.Background(), machine(), options)

	// assert
	require.Error(t, err)
	assert.NotErrorIs(t, err, launch.ErrSocketTimeout)
	assert.ErrorContains(t, err, exitingShare)
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Contains(t, stderr.String(), "cannot open the shared folder")
}

func TestRunWhenCancelledDuringStartup(t *testing.T) {
	// arrange
	f := fakes(t)
	t.Setenv("AIBOX_FAKE_VIRTIOFSD", "no-socket")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// act
	err := launch.Run(ctx, machine(), f.options())

	// assert
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRunWhenCancelledWhileQEMURuns(t *testing.T) {
	// arrange
	f := fakes(t)
	t.Setenv("AIBOX_FAKE_QEMU", "wait")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	// act
	go func() { done <- launch.Run(ctx, machine(), f.options()) }()

	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(f.records, "qemu"))

		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	cancel()

	// assert
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func TestRunReturnsWhileStdinStaysOpen(t *testing.T) {
	// arrange
	f := fakes(t)
	stdin, input, err := os.Pipe()
	require.NoError(t, err)

	defer func() { _ = input.Close() }()

	options := f.options()
	options.Stdin = stdin
	done := make(chan error, 1)

	// act
	go func() { done <- launch.Run(context.Background(), machine(), options) }()

	// assert
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after QEMU exited")
	}
}
