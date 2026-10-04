//go:build linux

package guest_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/guest"
)

func TestParseCmdline(t *testing.T) {
	// arrange
	tests := map[string]guest.Options{
		"root=/dev/vda rw console=ttyS0 quiet": {Console: "/dev/ttyS0"},
		"console=hvc0 aibox.shell":             {Console: "/dev/hvc0", Shell: true},
		"root=/dev/vda":                        {Console: "/dev/console"},
		"console=ttyS0 aibox.shell=1 panic=-1": {Console: "/dev/ttyS0", Shell: true},
		"console=tty0 console=ttyS0,115200n8":  {Console: "/dev/ttyS0"},
		"console= quiet":                       {Console: "/dev/console"},
		"console=ttyS0 aibox.proxy=4321":       {Console: "/dev/ttyS0", ProxyPort: 4321},
	}

	for cmdline, want := range tests {
		t.Run(cmdline, func(t *testing.T) {
			// act
			options, err := guest.ParseCmdline(cmdline)

			// assert
			require.NoError(t, err)
			assert.Equal(t, want, options)
		})
	}
}

func TestParseCmdlineRejectsABadProxyPort(t *testing.T) {
	for _, value := range []string{"x", "0", "4294967295", "99999999999", ""} {
		t.Run(value, func(t *testing.T) {
			// act
			options, err := guest.ParseCmdline("console=ttyS0 aibox.proxy=" + value)

			// assert
			assert.ErrorIs(t, err, guest.ErrBadProxyPort)
			assert.Equal(t, guest.Options{Console: "/dev/ttyS0"}, options)
		})
	}
}

func TestCommandRunsClaudeCodeAsTheUser(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty)

	// assert
	assert.Equal(t, []string{"/usr/local/bin/claude"}, cmd.Args)
	assert.Equal(t, "/project", cmd.Dir)
	assert.Contains(t, cmd.Env, "HOME=/home/user")
	assert.Contains(t, cmd.Env, "USER=user")
	assert.Contains(t, cmd.Env, "TERM=xterm-256color")
	assert.Contains(t, cmd.Env, "PATH=/usr/local/bin:/usr/bin:/bin")
	assert.Equal(t, &syscall.Credential{Uid: 1000, Gid: 1000}, cmd.SysProcAttr.Credential)
	assert.True(t, cmd.SysProcAttr.Setsid)
	assert.True(t, cmd.SysProcAttr.Setctty)
	assert.Equal(t, 0, cmd.SysProcAttr.Ctty)
	assert.Same(t, tty, cmd.Stdin)
	assert.Same(t, tty, cmd.Stdout)
	assert.Same(t, tty, cmd.Stderr)
}

func TestCommandPointsClaudeCodeAtTheProxy(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name(), ProxyPort: 4321}, tty)

	// assert
	assert.Contains(t, cmd.Env, "HTTPS_PROXY=http://127.0.0.1:3128")
	assert.Contains(t, cmd.Env, "https_proxy=http://127.0.0.1:3128")
	assert.Contains(t, cmd.Env, "NO_PROXY=localhost,127.0.0.1")
	assert.Contains(t, cmd.Env, "no_proxy=localhost,127.0.0.1")
}

func TestCommandWithoutAProxy(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty)

	// assert
	require.NotEmpty(t, cmd.Env)

	for _, variable := range cmd.Env {
		assert.NotContains(t, strings.ToUpper(variable), "PROXY")
	}
}

// forwarder runs Forward on a listener of its own and returns the address
// to connect to, the log and a function that stops it.
func forwarder(t *testing.T, dial func() (net.Conn, error)) (string, *strings.Builder, func() error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var log strings.Builder

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- guest.Forward(ctx, listener, dial, &log) }()

	stop := func() error {
		cancel()

		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("Forward did not return")

			return nil
		}
	}

	return listener.Addr().String(), &log, stop
}

func dialWithDeadline(t *testing.T, address string) net.Conn {
	t.Helper()

	conn, err := net.Dial("tcp", address)
	require.NoError(t, err)
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

func TestForwardJoinsEachClientWithTheHost(t *testing.T) {
	// arrange
	hostSide, forwarderSide := net.Pipe()
	address, _, stop := forwarder(t, func() (net.Conn, error) { return forwarderSide, nil })
	t.Cleanup(func() { assert.NoError(t, stop()) })
	client := dialWithDeadline(t, address)

	// act
	go func() { _, _ = io.WriteString(client, "to the host") }()

	toHost := make([]byte, 11)
	_, err := io.ReadFull(hostSide, toHost)
	require.NoError(t, err)

	go func() { _, _ = io.WriteString(hostSide, "to the client") }()

	toClient := make([]byte, 13)
	_, err = io.ReadFull(client, toClient)
	require.NoError(t, err)

	// assert
	assert.Equal(t, "to the host", string(toHost))
	assert.Equal(t, "to the client", string(toClient))
}

func TestForwardAnswersWith502WhenTheHostCannotBeReached(t *testing.T) {
	// arrange
	address, log, stop := forwarder(t, func() (net.Conn, error) { return nil, errors.New("vsock is down") })
	t.Cleanup(func() { assert.NoError(t, stop()) })
	client := dialWithDeadline(t, address)

	// act
	answer, err := io.ReadAll(client)

	// assert
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(answer), "HTTP/1.1 502 Bad Gateway\r\n"), string(answer))
	assert.Contains(t, log.String(), "vsock is down")
}

func TestForwardClosesTheTunnelsWhenTheContextEnds(t *testing.T) {
	// arrange
	hostSide, forwarderSide := net.Pipe()
	address, _, stop := forwarder(t, func() (net.Conn, error) { return forwarderSide, nil })
	client := dialWithDeadline(t, address)

	go func() { _, _ = io.WriteString(client, "hello") }()

	_, err := io.ReadFull(hostSide, make([]byte, 5))
	require.NoError(t, err)

	// act
	err = stop()

	// assert
	require.NoError(t, err)

	_, err = client.Read(make([]byte, 1))
	assert.Error(t, err)
}

func TestRunStartsTheForwarderWhenThereIsAProxy(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: "console=ttyS0 aibox.proxy=4321"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)

	calls := sys.callsCopy()
	assert.Contains(t, calls, "loopback up")
	assert.Contains(t, calls, "start /usr/local/bin/claude")

	listen := slices.Index(calls, "listen 127.0.0.1:3128")
	require.NotEqual(t, -1, listen)
	assert.Less(t, listen, slices.Index(calls, "start /usr/local/bin/claude"))

	// the forwarder is running on the listener and dials the host port
	client := dialWithDeadline(t, sys.listener.Addr().String())
	answer, err := io.ReadAll(client)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(answer), "HTTP/1.1 502"), string(answer))
	assert.Eventually(t, func() bool { return slices.Contains(sys.callsCopy(), "dial host 4321") }, 5*time.Second, 10*time.Millisecond)
}

func TestRunPowersOffWhenTheLoopbackStaysDown(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: "console=ttyS0 aibox.proxy=4321", failLoopback: errors.New("not permitted")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "loopback")
	assert.NotContains(t, sys.calls, "start /usr/local/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunReportsABadProxyPortAndGoesOnWithoutAProxy(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: "console=ttyS0 aibox.proxy=x"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Contains(t, sys.consoleOutput(), "aibox.proxy")
	assert.NotContains(t, sys.calls, "listen 127.0.0.1:3128")
	assert.Contains(t, sys.calls, "start /usr/local/bin/claude")
}

func TestRunWithoutAProxy(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: "console=ttyS0"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.NotContains(t, sys.calls, "loopback up")
	assert.NotContains(t, sys.calls, "listen 127.0.0.1:3128")
}

func TestRunPowersOffWhenTheProxyCannotListen(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: "console=ttyS0 aibox.proxy=4321", failListen: errors.New("address in use")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "address in use")
	assert.NotContains(t, sys.calls, "start /usr/local/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestCommandTurnsTheUpdaterOff(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty)

	// assert
	assert.Contains(t, cmd.Env, "DISABLE_AUTOUPDATER=1")
}

func TestCommandRunsAShellWhenAsked(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name(), Shell: true}, tty)

	// assert
	assert.Equal(t, []string{"/usr/bin/bash", "-l"}, cmd.Args)
	assert.Equal(t, uint32(1000), cmd.SysProcAttr.Credential.Uid)
}

func TestRunSetsUpTheVMThenRunsClaudeCodeAndPowersOff(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: "root=/dev/vda console=ttyS0"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"mount devtmpfs /dev",
		"mount proc /proc",
		"read cmdline",
		"open /dev/ttyS0",
		"mount sysfs /sys",
		"mount devpts /dev/pts",
		"mount tmpfs /dev/shm",
		"mount tmpfs /tmp",
		"mount tmpfs /run",
		"mount project /project",
		"mount home /home/user",
		"link /dev/fd -> /proc/self/fd",
		"link /dev/stdin -> /proc/self/fd/0",
		"link /dev/stdout -> /proc/self/fd/1",
		"link /dev/stderr -> /proc/self/fd/2",
		"hostname aibox",
		"start /usr/local/bin/claude",
		"wait",
		"halt",
	}, sys.calls)
	assert.Equal(t, mounted{"proc", "/proc", "proc", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, ""}, sys.mounts["/proc"])
	assert.Equal(t, mounted{"devpts", "/dev/pts", "devpts", syscall.MS_NOSUID | syscall.MS_NOEXEC, "mode=620,ptmxmode=666,gid=5"}, sys.mounts["/dev/pts"])
	assert.Equal(t, mounted{"tmpfs", "/tmp", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=1777"}, sys.mounts["/tmp"])
	assert.Equal(t, mounted{"project", "/project", "virtiofs", 0, ""}, sys.mounts["/project"])
}

func TestRunReapsOtherChildrenUntilClaudeCodeExits(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, orphans: 2}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Equal(t, 3, sys.waits)
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunPowersOffWhenAShareIsMissing(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failMount: "home"}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "home")
	assert.Contains(t, sys.consoleOutput(), "home")
	assert.NotContains(t, sys.calls, "start /usr/local/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunPowersOffWhenAnEarlyMountFails(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failMount: "devtmpfs"}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "devtmpfs")
	assert.Equal(t, []string{"mount devtmpfs /dev", "halt"}, sys.calls)
}

func TestRunPowersOffWithoutAConsole(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failOpen: errors.New("no such device")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "no such device")
	assert.NotContains(t, sys.calls, "start /usr/local/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunKeepsGoingWhenTheHostnameFails(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failHostname: errors.New("not permitted")}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Contains(t, sys.consoleOutput(), "not permitted")
	assert.Contains(t, sys.calls, "start /usr/local/bin/claude")
}

func TestRunPowersOffWhenClaudeCodeFails(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, exitCode: 7}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Contains(t, sys.consoleOutput(), "exit code 7")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunPowersOffWhenClaudeCodeCannotStart(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failStart: errors.New("no such file")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "no such file")
	assert.Contains(t, sys.consoleOutput(), "no such file")
	assert.NotContains(t, sys.calls, "wait")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunPowersOffWhenWaitFails(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failWait: errors.New("no child processes")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "no child processes")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunReportsAFailedHalt(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failHalt: errors.New("not permitted")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "halt")
}

func lastCall(t *testing.T, sys *fakeSystem) string {
	t.Helper()
	require.NotEmpty(t, sys.calls)

	return sys.calls[len(sys.calls)-1]
}

// newConsoleFile stands in for the terminal. The tests read it back to see
// what the init printed.
func newConsoleFile(t *testing.T) *os.File {
	t.Helper()

	tty, err := os.Create(filepath.Join(t.TempDir(), "ttyS0"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tty.Close() })

	return tty
}

type mounted struct {
	source, target, fstype string
	flags                  uintptr
	data                   string
}

type fakeSystem struct {
	t            *testing.T
	cmdline      string
	calls        []string
	mounts       map[string]mounted
	tty          *os.File
	exitCode     int
	orphans      int
	waits        int
	child        int
	listener     net.Listener
	mu           sync.Mutex
	failMount    string
	failLoopback error
	failListen   error
	failOpen     error
	failHostname error
	failStart    error
	failWait     error
	failHalt     error
}

func (s *fakeSystem) Mount(source, target, fstype string, flags uintptr, data string) error {
	s.record("mount " + source + " " + target)

	if s.mounts == nil {
		s.mounts = map[string]mounted{}
	}

	s.mounts[target] = mounted{source, target, fstype, flags, data}

	if source == s.failMount {
		return errors.New("no such device")
	}

	return nil
}

func (s *fakeSystem) Symlink(target, path string) error {
	s.record("link " + path + " -> " + target)

	return nil
}

func (s *fakeSystem) ReadCmdline() (string, error) {
	s.record("read cmdline")

	if s.cmdline == "" {
		return "console=ttyS0", nil
	}

	return s.cmdline, nil
}

func (s *fakeSystem) OpenConsole(path string) (*os.File, error) {
	s.record("open " + path)

	if s.failOpen != nil {
		return nil, s.failOpen
	}

	s.tty = newConsoleFile(s.t)

	return s.tty, nil
}

func (s *fakeSystem) BringLoopbackUp() error {
	s.record("loopback up")

	return s.failLoopback
}

// Listen listens on a free port instead of the proxy address, so that the
// tests do not take port 3128 on the machine they run on.
func (s *fakeSystem) Listen(address string) (net.Listener, error) {
	s.record("listen " + address)

	if s.failListen != nil {
		return nil, s.failListen
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = listener.Close() })
	s.listener = listener

	return listener, nil
}

func (s *fakeSystem) DialHost(port uint32) (net.Conn, error) {
	s.record(fmt.Sprintf("dial host %d", port))

	return nil, errors.New("no host in the test")
}

// record is for the calls the forwarder goroutine makes while the test reads
// the list.
func (s *fakeSystem) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, call)
}

func (s *fakeSystem) callsCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.calls)
}

func (s *fakeSystem) Sethostname(name string) error {
	s.record("hostname " + name)

	return s.failHostname
}

func (s *fakeSystem) Start(cmd *exec.Cmd) (int, error) {
	s.record("start " + cmd.Path)

	if s.failStart != nil {
		return 0, s.failStart
	}

	s.child = 4242

	return s.child, nil
}

func (s *fakeSystem) Wait() (int, int, error) {
	s.record("wait")
	s.waits++

	if s.failWait != nil {
		return 0, 0, s.failWait
	}

	if s.waits <= s.orphans {
		return 100 + s.waits, 0, nil
	}

	return s.child, s.exitCode, nil
}

func (s *fakeSystem) Halt() error {
	s.record("halt")

	return s.failHalt
}

func (s *fakeSystem) consoleOutput() string {
	if s.tty == nil {
		return ""
	}

	content, err := os.ReadFile(s.tty.Name())
	require.NoError(s.t, err)

	return string(content)
}
