package launch_test

import (
	"bufio"
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
	"github.com/the127/aibox/internal/session"
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
	stderr := &syncBuffer{}
	options := f.options()
	options.Stderr = stderr

	// act
	err := launch.Run(context.Background(), machine(), options)

	// assert
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "fake qemu ran")
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

// cmdlinePort is the value of the word on the kernel command line of the
// VM, or the test fails.
func cmdlinePort(t *testing.T, f *fakeProcesses, word string) string {
	t.Helper()

	for _, w := range f.record(t, "qemu-cmdline") {
		if port, ok := strings.CutPrefix(w, word+"="); ok {
			return port
		}
	}

	t.Fatalf("no %s on the kernel command line", word)

	return ""
}

func TestRunTellsTheVMTheTerminalPort(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(), f.options())

	// assert
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(f.terminal(t).Addr().(*net.TCPAddr).Port), cmdlinePort(t, f, "aibox.terminal"))
}

// printingProcess is the command in the VM as the fake guest runs it: it
// prints its output and exits with the code.
type printingProcess struct {
	output *strings.Reader
	code   int
}

func (p *printingProcess) Read(b []byte) (int, error)  { return p.output.Read(b) }
func (p *printingProcess) Write(b []byte) (int, error) { return len(b), nil }
func (*printingProcess) Resize(session.Size) error     { return nil }
func (p *printingProcess) Wait() (int, error)          { return p.code, nil }
func (*printingProcess) Close() error                  { return nil }

// guestServes connects to the terminal listener as the VM and serves a
// session whose command prints the output and exits with the code. The
// returned channel gets the result of serving once the session is over.
func guestServes(t *testing.T, listener net.Listener, output string, code int) <-chan error {
	t.Helper()

	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	start := func(session.Terminal) (session.Process, error) { //nolint:unparam // the signature is session.Starter
		return &printingProcess{output: strings.NewReader(output), code: code}, nil
	}

	served := make(chan error, 1)

	go func() { served <- session.Serve(conn, start) }()

	return served
}

func sessionOver(t *testing.T, served <-chan error) {
	t.Helper()

	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
	}
}

func TestRunAttachesTheTerminalToTheSessionOfTheVM(t *testing.T) {
	// arrange
	f := fakes(t)
	stdin, input, err := os.Pipe()
	require.NoError(t, err)

	defer func() { _ = input.Close() }()

	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	f.stdin, f.stdout, f.stderr = stdin, stdout, stderr
	_, stop := running(t, f)
	t.Cleanup(func() { assert.ErrorIs(t, stop(), context.Canceled) })

	// act
	sessionOver(t, guestServes(t, f.terminal(t), "hello from the VM\r\n", 3))

	// assert
	assert.Equal(t, "hello from the VM\r\n", stdout.String())
	assert.Eventually(t, func() bool { return strings.Contains(stderr.String(), "exit code 3") }, 5*time.Second, 10*time.Millisecond)
}

func TestRunSaysNothingWhenTheCommandInTheVMSucceeds(t *testing.T) {
	// arrange
	f := fakes(t)
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	f.stdout, f.stderr = stdout, stderr
	_, stop := running(t, f)
	t.Cleanup(func() { assert.ErrorIs(t, stop(), context.Canceled) })

	// act
	sessionOver(t, guestServes(t, f.terminal(t), "bye\r\n", 0))

	// assert
	assert.Equal(t, "bye\r\n", stdout.String())
	assert.NotContains(t, stderr.String(), "aibox:")
}

// waitingProcess is a command in the VM that never exits on its own.
type waitingProcess struct {
	printingProcess
	started chan struct{}
	closed  chan struct{}
}

func (p *waitingProcess) Wait() (int, error) {
	<-p.closed

	return 0, nil
}

func (p *waitingProcess) Close() error {
	close(p.closed)

	return nil
}

func TestRunEndsASessionStillRunningWhenItIsStoppedAndSaysNothing(t *testing.T) {
	// arrange
	f := fakes(t)
	stderr := &syncBuffer{}
	f.stderr = stderr
	_, stop := running(t, f)

	conn, err := net.Dial("tcp", f.terminal(t).Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	process := &waitingProcess{started: make(chan struct{}), closed: make(chan struct{})}
	process.output = strings.NewReader("")
	served := make(chan error, 1)

	go func() {
		served <- session.Serve(conn, func(session.Terminal) (session.Process, error) {
			close(process.started)

			return process, nil
		})
	}()

	select {
	case <-process.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the session was not attached")
	}

	// act
	err = stop()

	// assert
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, stderr.String(), "aibox:")

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the session in the VM did not end")
	}
}

func TestRunPointsAtTheConsoleLogWhenTheVMEndsWithoutATerminal(t *testing.T) {
	// arrange
	f := fakes(t)
	stderr := &syncBuffer{}
	f.stderr = stderr
	m := machine()
	m.ConsoleLog = "/home/someone/.aibox/projects/p/console.log"

	// act
	err := launch.Run(context.Background(), m, f.options())

	// assert
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "/home/someone/.aibox/projects/p/console.log")
}

func TestRunTellsTheVMTheProxyPort(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(), f.options())

	// assert
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(f.listener(t).Addr().(*net.TCPAddr).Port), cmdlinePort(t, f, "aibox.proxy"))
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
