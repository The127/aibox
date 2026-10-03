package launch_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/vm"
)

const guestCID = 42

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
		GuestCID: guestCID,
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

func proxyPort(t *testing.T, f *fakeProcesses) string {
	t.Helper()

	for _, word := range f.record(t, "qemu-cmdline") {
		if port, ok := strings.CutPrefix(word, "aibox.proxy="); ok {
			return port
		}
	}

	t.Fatal("no aibox.proxy on the kernel command line")

	return ""
}

func TestRunTellsTheVMTheProxyPort(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(), f.options())

	// assert
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(f.listener(t).Addr().(*net.TCPAddr).Port), proxyPort(t, f))
}

// running starts Run with a QEMU that waits to be stopped, and returns the
// proxy listener and a function that stops the VM and returns Run's error.
func running(t *testing.T, f *fakeProcesses) (net.Listener, func() error) {
	t.Helper()
	t.Setenv("AIBOX_FAKE_QEMU", "wait")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- launch.Run(ctx, machine(), f.options()) }()

	listener := f.listener(t)

	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(f.records, "qemu"))

		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "QEMU did not start")

	return listener, func() error {
		cancel()

		return <-done
	}
}

func TestRunAnswersTheProxyRequestsOfTheVMWhileQEMURuns(t *testing.T) {
	// arrange
	f := fakes(t)
	listener, stop := running(t, f)
	t.Cleanup(func() { assert.ErrorIs(t, stop(), context.Canceled) })

	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))

	// act
	_, err = io.WriteString(conn, "CONNECT :443 HTTP/1.1\r\nHost: :443\r\n\r\n")
	require.NoError(t, err)

	status, err := bufio.NewReader(conn).ReadString('\n')

	// assert
	require.NoError(t, err)
	assert.Equal(t, "HTTP/1.1 400 Bad Request\r\n", status)
}

func TestRunClosesTheProxyListenerWhenItReturns(t *testing.T) {
	// arrange
	f := fakes(t)
	listener, stop := running(t, f)

	// act
	err := stop()

	// assert
	require.ErrorIs(t, err, context.Canceled)

	_, err = listener.Accept()
	assert.ErrorIs(t, err, net.ErrClosed)
}

func TestRunStopsVirtiofsdAfterQEMU(t *testing.T) {
	// arrange
	f := fakes(t)
	_, stop := running(t, f)

	// act
	err := stop()

	// assert
	require.ErrorIs(t, err, context.Canceled)

	qemuExited, err := os.Stat(filepath.Join(f.records, "qemu-exited"))
	require.NoError(t, err)

	daemonStopped, err := os.Stat(filepath.Join(f.records, "virtiofsd-project.sock-stopped"))
	require.NoError(t, err)
	assert.False(t, daemonStopped.ModTime().Before(qemuExited.ModTime()), "virtiofsd stopped before QEMU exited")
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
