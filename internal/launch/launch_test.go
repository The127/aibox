package launch_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/sandbox"
	"github.com/the127/aibox/internal/session"
	"github.com/the127/aibox/internal/vm"
)

// machine is a VM whose kernel and root disk are empty files of the test.
func machine(t *testing.T) vm.Machine {
	t.Helper()

	image := t.TempDir()
	for _, name := range []string{"vmlinuz", "os.ext4"} {
		require.NoError(t, os.WriteFile(filepath.Join(image, name), nil, 0o600))
	}

	return vm.Machine{
		Kernel:    filepath.Join(image, "vmlinuz"),
		Rootfs:    filepath.Join(image, "os.ext4"),
		MemoryMiB: 512,
		CPUs:      1,
		Shares: []vm.Share{
			{Tag: "project", Dir: "/home/someone/project"},
			{Tag: "home", Dir: "/home/someone/.aibox/home"},
		},
	}
}

func TestRunStartsVirtiofsdForEachShareBeforeQEMU(t *testing.T) {
	// arrange
	f := fakes(t)
	stderr := &syncBuffer{}
	options := f.options()
	options.Stderr = stderr

	// act
	err := launch.Run(context.Background(), machine(t), options)

	// assert
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "fake qemu ran")
}

// argAfter is the value that follows the flag in the arguments, or the test
// fails.
func argAfter(t *testing.T, args []string, flag string) string {
	t.Helper()

	i := slices.Index(args, flag)
	require.NotEqual(t, -1, i, "no %s in %v", flag, args)
	require.Less(t, i+1, len(args), "nothing after %s", flag)

	return args[i+1]
}

func TestRunWrapsQEMUInBubblewrap(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(t), f.options())

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.record(t, "bwrap"), "--unshare-all")
	assert.Equal(t, []string{f.qemu}, f.record(t, "qemu-program"))
	assert.Equal(t, sandbox.Kernel, argAfter(t, f.record(t, "qemu"), "-kernel"))
}

func TestRunGivesBubblewrapTheKernelToCopy(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(t), f.options())

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"file"}, f.record(t, "bwrap-kernel-kind"))
}

func TestRunGivesQEMUNoEnvironmentInTheSandbox(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(t), f.options())

	// assert
	require.NoError(t, err)

	for _, variable := range f.record(t, "qemu-environment") {
		assert.True(t, strings.HasPrefix(variable, "TMPDIR=") || strings.HasPrefix(variable, "AIBOX_FAKE_"), variable)
	}
}

func TestRunWithoutTheSandboxRunsQEMUAlone(t *testing.T) {
	// arrange
	f := fakes(t)
	options := f.options()
	options.NoSandbox = true

	// act
	err := launch.Run(context.Background(), machine(t), options)

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(f.records, "bwrap"))
	assert.True(t, strings.HasPrefix(argAfter(t, f.record(t, "qemu"), "-kernel"), "/dev/fd/"))
}

func TestRunFailsWithoutBubblewrapAndNamesTheWayAround(t *testing.T) {
	// arrange
	f := fakes(t)
	options := f.options()
	options.Bubblewrap = filepath.Join(t.TempDir(), "no-bwrap")

	// act
	err := launch.Run(context.Background(), machine(t), options)

	// assert
	require.ErrorContains(t, err, "--no-sandbox")
	assert.NoFileExists(t, filepath.Join(f.records, "qemu"))
}

func TestRunGivesQEMUTheRightFileInEachSlot(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(t), f.options())

	// assert
	require.NoError(t, err)

	// the KVM device, the console, the root disk, two shares and the vhost
	// device, in the order QEMU's arguments name them
	assert.Equal(t, []string{"device", "socket", "file", "socket", "socket", "device"}, f.record(t, "qemu-fd-kinds"))
	assert.Contains(t, f.record(t, "virtiofsd-project.sock"), "--shared-dir=/home/someone/project")
	assert.Contains(t, f.record(t, "virtiofsd-home.sock"), "--shared-dir=/home/someone/.aibox/home")
}

func TestRunStopsVirtiofsdAndRemovesTheSockets(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(t), f.options())

	// assert
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(f.records, "virtiofsd-project.sock-stopped"))
	assert.FileExists(t, filepath.Join(f.records, "virtiofsd-home.sock-stopped"))
	assert.NoDirExists(t, socketDir(t, f.record(t, "virtiofsd-project.sock")))
}

func TestRunWritesTheConsoleOfTheVMIntoTheLog(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(t), f.options())

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(f.consoleLog)
	require.NoError(t, err)
	assert.Equal(t, "hello from the console\n", string(content))
}

func TestRunReturnsWhenQEMUCannotStart(t *testing.T) {
	// arrange
	f := fakes(t)
	options := f.options()
	options.QEMU = filepath.Join(t.TempDir(), "no-qemu")
	done := make(chan error, 1)

	// act
	m := machine(t)

	go func() { done <- launch.Run(context.Background(), m, options) }()

	// assert
	select {
	case err := <-done:
		assert.ErrorContains(t, err, "no-qemu")
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestRunFailsBeforeQEMUWhenTheKernelCannotBeOpened(t *testing.T) {
	// arrange
	f := fakes(t)
	m := machine(t)
	m.Kernel = filepath.Join(t.TempDir(), "gone")

	// act
	err := launch.Run(context.Background(), m, f.options())

	// assert
	require.ErrorContains(t, err, m.Kernel)
	assert.NoFileExists(t, filepath.Join(f.records, "qemu"))
}

func TestRunWhenQEMUFails(t *testing.T) {
	// arrange
	f := fakes(t)
	t.Setenv("AIBOX_FAKE_QEMU_EXIT", "3")

	// act
	err := launch.Run(context.Background(), machine(t), f.options())

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
	err := launch.Run(context.Background(), machine(t), options)

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
	err := launch.Run(context.Background(), machine(t), options)

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
	err := launch.Run(ctx, machine(t), f.options())

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
	m := machine(t)

	go func() { done <- launch.Run(ctx, m, f.options()) }()

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
	err := launch.Run(context.Background(), machine(t), f.options())

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

	start := func(session.Request) (session.Process, error) { //nolint:unparam // the signature is session.Starter
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

func TestRunHandsTheEnvToTheSessionOfTheVM(t *testing.T) {
	// arrange
	f := fakes(t)
	f.env = []string{"GOFLAGS=-mod=mod"}
	_, stop := running(t, f)
	t.Cleanup(func() { assert.ErrorIs(t, stop(), context.Canceled) })

	conn, err := net.Dial("tcp", f.terminal(t).Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	asked := make(chan session.Request, 1)

	// act
	go func() {
		_ = session.Serve(conn, func(request session.Request) (session.Process, error) {
			asked <- request

			return &printingProcess{output: strings.NewReader("")}, nil
		})
	}()

	// assert
	select {
	case request := <-asked:
		assert.Equal(t, []string{"GOFLAGS=-mod=mod"}, request.Env)
	case <-time.After(5 * time.Second):
		t.Fatal("the session was not started")
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
		served <- session.Serve(conn, func(session.Request) (session.Process, error) {
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

	// act
	err := launch.Run(context.Background(), machine(t), f.options())

	// assert
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), f.consoleLog)
}

func TestRunTellsTheVMTheProxyPort(t *testing.T) {
	// arrange
	f := fakes(t)

	// act
	err := launch.Run(context.Background(), machine(t), f.options())

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

	m := machine(t)

	go func() { done <- launch.Run(ctx, m, f.options()) }()

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
	m := machine(t)

	// act
	go func() { done <- launch.Run(context.Background(), m, options) }()

	// assert
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after QEMU exited")
	}
}
