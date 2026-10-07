//go:build linux

package guest_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/guest"
	"github.com/the127/aibox/internal/session"
)

// terminalPort is the vsock port the fake host serves the terminal on.
const terminalPort = 5432

// withTerminal is the kernel command line of a VM whose host serves a
// terminal and nothing else.
var withTerminal = fmt.Sprintf("console=hvc0 aibox.terminal=%d", terminalPort)

func TestParseCmdline(t *testing.T) {
	// arrange
	tests := map[string]guest.Options{
		"root=/dev/vda rw console=ttyS0 quiet":   {Console: "/dev/ttyS0"},
		"console=hvc0 aibox.shell":               {Console: "/dev/hvc0", Shell: true},
		"root=/dev/vda":                          {Console: "/dev/console"},
		"console=ttyS0 aibox.shell=1 panic=-1":   {Console: "/dev/ttyS0", Shell: true},
		"console=tty0 console=ttyS0,115200n8":    {Console: "/dev/ttyS0"},
		"console= quiet":                         {Console: "/dev/console"},
		"console=ttyS0 aibox.proxy=4321":         {Console: "/dev/ttyS0", ProxyPort: 4321},
		"console=hvc0 aibox.terminal=5432":       {Console: "/dev/hvc0", TerminalPort: 5432},
		"console=hvc0 aibox.loopback=64422,3000": {Console: "/dev/hvc0", Loopback: []uint16{64422, 3000}},
		"console=hvc0 aibox.mount=mount0:/opt/go aibox.mount=mount1:/opt/bin": {
			Console: "/dev/hvc0",
			Mounts:  []guest.Mount{{Tag: "mount0", Path: "/opt/go"}, {Tag: "mount1", Path: "/opt/bin"}},
		},
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

func TestParseCmdlineRejectsAMountItCannotRead(t *testing.T) {
	for _, value := range []string{"mount0", "mount0:", ":/opt/go", "mount0:opt/go", "mount0:/", "mount0:/opt/go:x"} {
		t.Run(value, func(t *testing.T) {
			// act
			options, err := guest.ParseCmdline("console=hvc0 aibox.mount=" + value)

			// assert
			assert.ErrorIs(t, err, guest.ErrBadMountWord)
			assert.ErrorContains(t, err, value)
			assert.Equal(t, guest.Options{Console: "/dev/hvc0"}, options)
		})
	}
}

func TestParseCmdlineRejectsABadPort(t *testing.T) {
	for _, word := range []string{"aibox.proxy", "aibox.terminal"} {
		for _, value := range []string{"x", "0", "4294967295", "99999999999", ""} {
			t.Run(word+"="+value, func(t *testing.T) {
				// act
				options, err := guest.ParseCmdline("console=ttyS0 " + word + "=" + value)

				// assert
				assert.ErrorIs(t, err, guest.ErrBadPort)
				assert.ErrorContains(t, err, word)
				assert.Equal(t, guest.Options{Console: "/dev/ttyS0"}, options)
			})
		}
	}
}

func TestParseCmdlineRejectsBadLoopbackPorts(t *testing.T) {
	for _, value := range []string{"", "x", "0", "65536", "80,", "80,,443", "-1"} {
		t.Run(value, func(t *testing.T) {
			// act
			options, err := guest.ParseCmdline("console=hvc0 aibox.loopback=" + value)

			// assert
			assert.ErrorIs(t, err, guest.ErrBadLoopback)
			assert.Equal(t, guest.Options{Console: "/dev/hvc0"}, options)
		})
	}
}

func TestCommandRunsClaudeCodeAsTheUser(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty, session.Request{Term: "xterm-kitty"})

	// assert
	assert.Equal(t, []string{"/usr/bin/claude", "--append-system-prompt-file", "/etc/aibox/prompt.md"}, cmd.Args)
	assert.Equal(t, "/project", cmd.Dir)
	assert.Contains(t, cmd.Env, "AIBOX=1")
	assert.Contains(t, cmd.Env, "HOME=/home/user")
	assert.Contains(t, cmd.Env, "USER=user")
	assert.Contains(t, cmd.Env, "TERM=xterm-kitty")
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
	cmd := guest.Command(guest.Options{Console: tty.Name(), ProxyPort: 4321}, tty, session.Request{Term: "xterm"})

	// assert
	assert.Contains(t, cmd.Env, "HTTPS_PROXY=http://127.0.0.1:3128")
	assert.Contains(t, cmd.Env, "https_proxy=http://127.0.0.1:3128")
	assert.Contains(t, cmd.Env, "NO_PROXY=localhost,127.0.0.1")
	assert.Contains(t, cmd.Env, "no_proxy=localhost,127.0.0.1")
}

func TestCommandPutsThePathOfTheHostBeforeTheImage(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)
	request := session.Request{Term: "xterm", Env: []string{"PATH=/opt/go/bin:/opt/bin", "GOFLAGS=-mod=mod"}}

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty, request)

	// assert
	assert.Contains(t, cmd.Env, "PATH=/opt/go/bin:/opt/bin:/usr/local/bin:/usr/bin:/bin")
}

func TestCommandLeavesOutFoldersOfTheHostThatAreNotAbsolute(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)
	request := session.Request{Term: "xterm", Env: []string{"PATH=rel/bin::/:/opt/bin"}}

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty, request)

	// assert
	assert.Contains(t, cmd.Env, "PATH=/opt/bin:/usr/local/bin:/usr/bin:/bin")
}

func TestCommandTakesTheVariablesOfTheHost(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)
	request := session.Request{Term: "xterm", Env: []string{"GOFLAGS=-mod=mod", "TOKEN=s3cret=with=equals"}}

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty, request)

	// assert
	assert.Contains(t, cmd.Env, "GOFLAGS=-mod=mod")
	assert.Contains(t, cmd.Env, "TOKEN=s3cret=with=equals")
}

func TestRunKeepsItsOwnVariablesOverThoseOfTheHost(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, clientEnv: []string{"HOME=/elsewhere", "TERM=vt100", "PATH=/opt/bin", "GOFLAGS=-mod=mod"}}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Contains(t, sys.started.Env, "HOME=/home/user")
	assert.Contains(t, sys.started.Env, "TERM=xterm-kitty")
	assert.Contains(t, sys.started.Env, "PATH=/opt/bin:/usr/local/bin:/usr/bin:/bin")
	assert.NotContains(t, sys.started.Env, "HOME=/elsewhere")
	assert.NotContains(t, sys.started.Env, "TERM=vt100")
	assert.Contains(t, sys.started.Env, "GOFLAGS=-mod=mod")
}

func TestRunTellsTheConsoleAboutVariablesItDropped(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, clientEnv: []string{"HOME=/elsewhere", "TERM=vt100", "PATH=/opt/bin", "GOFLAGS=-mod=mod"}}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Contains(t, sys.consoleOutput(), "aibox: HOME, TERM stay as the VM sets them")
	assert.NotContains(t, sys.consoleOutput(), "GOFLAGS")
	assert.NotContains(t, sys.consoleOutput(), "PATH")
}

func TestCommandWithoutAProxy(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty, session.Request{Term: "xterm"})

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
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.proxy=4321"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)

	calls := sys.callsCopy()
	assert.Contains(t, calls, "loopback up")
	assert.Contains(t, calls, "start /usr/bin/claude")

	listen := slices.Index(calls, "listen 127.0.0.1:3128")
	require.NotEqual(t, -1, listen)
	assert.Less(t, listen, slices.Index(calls, "start /usr/bin/claude"))

	// the forwarder is running on the listener and dials the host port
	client := dialWithDeadline(t, sys.listener.Addr().String())
	answer, err := io.ReadAll(client)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(answer), "HTTP/1.1 502"), string(answer))
	assert.Eventually(t, func() bool { return slices.Contains(sys.callsCopy(), "dial host 4321") }, 5*time.Second, 10*time.Millisecond)
}

// hostProxy is the proxy on the host for one connection. It reads the
// CONNECT request into the channel and writes the answer.
func hostProxy(t *testing.T, answer string) (func() (net.Conn, error), <-chan string) {
	t.Helper()

	requests := make(chan string, 1)

	return func() (net.Conn, error) {
		hostSide, guestSide := net.Pipe()
		t.Cleanup(func() { _ = hostSide.Close() })

		go func() {
			var request []byte

			b := make([]byte, 1)
			for !strings.HasSuffix(string(request), "\r\n\r\n") {
				if _, err := hostSide.Read(b); err != nil {
					return
				}

				request = append(request, b[0])
			}

			// the tests look at the first request only
			select {
			case requests <- string(request):
			default:
			}

			_, _ = io.WriteString(hostSide, answer)
		}()

		return guestSide, nil
	}, requests
}

func TestRunTunnelsALoopbackPortOfTheHostThroughTheProxy(t *testing.T) {
	// arrange
	proxy, requests := hostProxy(t, "HTTP/1.1 200 Connection Established\r\n\r\nhello from the host")
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.proxy=4321 aibox.loopback=64422", proxyPort: 4321, proxy: proxy}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)

	listener, ok := sys.listeners["127.0.0.1:64422"]
	require.True(t, ok, "listens on the same port of its own loopback")

	client := dialWithDeadline(t, listener.Addr().String())

	// the server behind the tunnel speaks first, and its bytes reach the
	// client even though they came with the answer of the proxy
	greeting := make([]byte, len("hello from the host"))
	_, err = io.ReadFull(client, greeting)
	require.NoError(t, err)
	assert.Equal(t, "hello from the host", string(greeting))
	assert.Equal(t, "CONNECT localhost:64422 HTTP/1.1\r\nHost: localhost:64422\r\n\r\n", <-requests)
}

func TestRunClosesAClientOfAPortTheProxyRefuses(t *testing.T) {
	// arrange
	proxy, _ := hostProxy(t, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.proxy=4321 aibox.loopback=64422", proxyPort: 4321, proxy: proxy}
	require.NoError(t, guest.Run(sys))

	// act
	client := dialWithDeadline(t, sys.listeners["127.0.0.1:64422"].Addr().String())
	answer, err := io.ReadAll(client)

	// assert
	require.NoError(t, err)
	assert.Empty(t, answer, "the client may not speak HTTP, so it gets nothing")
	assert.Eventually(t, func() bool {
		return strings.Contains(sys.consoleOutput(), `the proxy answered "HTTP/1.1 403 Forbidden" for localhost:64422`)
	}, 5*time.Second, 10*time.Millisecond)
}

func TestRunTellsTheConsoleAboutARefusedPortOnlyOnce(t *testing.T) {
	// arrange
	proxy, _ := hostProxy(t, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.proxy=4321 aibox.loopback=64422", proxyPort: 4321, proxy: proxy}
	require.NoError(t, guest.Run(sys))

	// act
	for range 3 {
		client := dialWithDeadline(t, sys.listeners["127.0.0.1:64422"].Addr().String())
		_, err := io.ReadAll(client)
		require.NoError(t, err)
	}

	// assert
	message := `the proxy answered "HTTP/1.1 502 Bad Gateway" for localhost:64422`
	assert.Eventually(t, func() bool { return strings.Contains(sys.consoleOutput(), message) }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, strings.Count(sys.consoleOutput(), message))
}

func TestRunTellsTheConsoleAboutARefusalAgainAfterASuccess(t *testing.T) {
	// arrange
	refused, _ := hostProxy(t, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
	accepted, _ := hostProxy(t, "HTTP/1.1 200 Connection Established\r\n\r\n")
	answers := []func() (net.Conn, error){refused, accepted, refused}

	var calls atomic.Int32

	proxy := func() (net.Conn, error) { return answers[calls.Add(1)-1]() }
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.proxy=4321 aibox.loopback=64422", proxyPort: 4321, proxy: proxy}
	require.NoError(t, guest.Run(sys))

	// act
	for range answers {
		client := dialWithDeadline(t, sys.listeners["127.0.0.1:64422"].Addr().String())
		_ = client.(*net.TCPConn).CloseWrite()
		_, err := io.ReadAll(client)
		require.NoError(t, err)
	}

	// assert
	message := `the proxy answered "HTTP/1.1 502 Bad Gateway" for localhost:64422`
	assert.Eventually(t, func() bool { return strings.Count(sys.consoleOutput(), message) == 2 }, 5*time.Second, 10*time.Millisecond)
}

func TestRunListensOnTheLoopbackPortsOnlyWithAProxy(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.loopback=64422"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.NotContains(t, sys.callsCopy(), "listen 127.0.0.1:64422")
}

func TestRunPowersOffWhenTheLoopbackStaysDown(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.proxy=4321", failLoopback: errors.New("not permitted")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "loopback")
	assert.NotContains(t, sys.calls, "start /usr/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunReportsABadProxyPortAndGoesOnWithoutAProxy(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.proxy=x"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Contains(t, sys.consoleOutput(), "aibox.proxy")
	assert.NotContains(t, sys.calls, "listen 127.0.0.1:3128")
	assert.Contains(t, sys.calls, "start /usr/bin/claude")
}

func TestRunWithoutAProxy(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: withTerminal}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.NotContains(t, sys.calls, "loopback up")
	assert.NotContains(t, sys.calls, "listen 127.0.0.1:3128")
}

func TestRunPowersOffWhenTheProxyCannotListen(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.proxy=4321", failListen: errors.New("address in use")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "address in use")
	assert.NotContains(t, sys.calls, "start /usr/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestCommandTurnsNonessentialTrafficOff(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty, session.Request{Term: "xterm"})

	// assert
	assert.Contains(t, cmd.Env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
}

func TestCommandKeepsTheGoModuleCacheOnTheStateDisk(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name()}, tty, session.Request{Term: "xterm"})

	// assert
	assert.Contains(t, cmd.Env, "GOMODCACHE=/home/user/.cache/go-mod")
}

func TestCommandRunsAShellWhenAsked(t *testing.T) {
	// arrange
	tty := newConsoleFile(t)

	// act
	cmd := guest.Command(guest.Options{Console: tty.Name(), Shell: true}, tty, session.Request{Term: "xterm"})

	// assert
	assert.Equal(t, []string{"/usr/bin/bash", "-l"}, cmd.Args)
	assert.Equal(t, uint32(1000), cmd.SysProcAttr.Credential.Uid)
}

func TestRunSetsUpTheVMThenRunsClaudeCodeAndPowersOff(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: "root=/dev/vda " + withTerminal}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Equal(t, append(slices.Clone(overlayCalls),
		"mount devtmpfs /dev",
		"mount proc /proc",
		"read cmdline",
		"open /dev/hvc0",
		"mount sysfs /sys",
		"mount cgroup2 /sys/fs/cgroup",
		"mount devpts /dev/pts",
		"mount tmpfs /dev/shm",
		"mount tmpfs /tmp",
		"mount tmpfs /var/tmp",
		"mount tmpfs /run",
		"mount project /project",
		"mount home /home/user",
		"controllers /sys/fs/cgroup",
		"delegate /sys/fs/cgroup/user",
		"controllers /sys/fs/cgroup/user",
		"delegate /sys/fs/cgroup/user/session",
		"chmod 666 /dev/kvm",
		"chmod 666 /dev/fuse",
		"blank /dev/vdb",
		"format /dev/vdb",
		"mount /dev/vdb /var/lib/aibox/state",
		"own /var/lib/aibox/state/local",
		"mount /var/lib/aibox/state/local /usr/local",
		"own /var/lib/aibox/state/cache",
		"mount /var/lib/aibox/state/cache /home/user/.cache",
		"own /var/lib/aibox/state/containers",
		"mount /var/lib/aibox/state/containers /home/user/.local/share/containers",
		"own /usr/local/bin",
		"mount shared /",
		"mount overlay /",
		"link /dev/fd -> /proc/self/fd",
		"link /dev/stdin -> /proc/self/fd/0",
		"link /dev/stdout -> /proc/self/fd/1",
		"link /dev/stderr -> /proc/self/fd/2",
		"hostname aibox",
		"dial host 5432",
		"start /usr/bin/claude",
		"wait",
		"halt",
	), sys.calls)
	assert.Equal(t, 0, sys.exitCodeOnTheHost(t))
	assert.Equal(t, mounted{"proc", "/proc", "proc", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, ""}, sys.mounts["/proc"])
	assert.Equal(t, mounted{"devpts", "/dev/pts", "devpts", syscall.MS_NOSUID | syscall.MS_NOEXEC, "mode=620,ptmxmode=666,gid=5"}, sys.mounts["/dev/pts"])
	assert.Equal(t, mounted{"tmpfs", "/tmp", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=1777"}, sys.mounts["/tmp"])
	assert.Equal(t, mounted{"project", "/project", "virtiofs", 0, ""}, sys.mounts["/project"])
	assert.Equal(t, mounted{"/dev/vdb", "/var/lib/aibox/state", "ext4", syscall.MS_NOSUID | syscall.MS_NODEV, ""}, sys.mounts["/var/lib/aibox/state"])
	assert.Equal(t, mounted{"/var/lib/aibox/state/local", "/usr/local", "", syscall.MS_BIND, ""}, sys.mounts["/usr/local"])
	assert.Equal(t, mounted{"cgroup2", "/sys/fs/cgroup", "cgroup2", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "nsdelegate"}, sys.mounts["/sys/fs/cgroup"])
	assert.Equal(t, mounted{"overlay", "/run/root", "overlay", 0, "lowerdir=/,upperdir=/run/upper,workdir=/run/work"}, sys.mounts["/run/root"])
	assert.Equal(t, mounted{"overlay", "/", "", syscall.MS_REMOUNT | syscall.MS_BIND | syscall.MS_RDONLY, ""}, sys.mounts["/"])
	assert.Equal(t, mounted{"shared", "/", "", syscall.MS_REC | syscall.MS_SHARED, ""}, sys.mountsOf["/"][0])
	assert.Equal(t, "/sys/fs/cgroup/user/session", sys.startedIn)
}

func TestRunPowersOffWhenTheOverlayCannotBecomeTheRoot(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failPivot: true}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "make /run/root the root: invalid argument")
	assert.Equal(t, append(slices.Clone(overlayCalls), "halt"), sys.calls)
}

func TestRunReportsAnErrorBeforeTheConsoleOnStandardError(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failPivot: true}

	// act
	err := guest.Run(sys)

	// assert
	require.Error(t, err)
	assert.Contains(t, sys.stderr.String(), "aibox: make /run/root the root: invalid argument")
}

func TestRunReportsAnErrorOnTheConsoleOnlyOnceItIsOpen(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failDelegate: errors.New("no such file or directory")}

	// act
	err := guest.Run(sys)

	// assert
	require.Error(t, err)
	assert.Contains(t, sys.consoleOutput(), "no such file or directory")
	assert.Empty(t, sys.stderr.String())
}

func TestRunPowersOffWhenTheCgroupOfTheUserCannotBeMade(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failDelegate: errors.New("no such file or directory")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "/sys/fs/cgroup/user")
	assert.NotContains(t, sys.calls, "start /usr/bin/claude")
	assert.Equal(t, "halt", sys.calls[len(sys.calls)-1])
}

func TestRunSkipsADeviceTheKernelDidNotMake(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, files: []string{"/dev/fuse"}}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Contains(t, sys.calls, "chmod 666 /dev/kvm")
	assert.Contains(t, sys.calls, "start /usr/bin/claude")
}

func TestRunPowersOffWhenADeviceCannotBeOpenedToEveryone(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, files: []string{"/dev/kvm"}, failChmod: errors.New("read-only file system")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "/dev/kvm")
	assert.ErrorContains(t, err, "read-only file system")
	assert.Equal(t, "halt", sys.calls[len(sys.calls)-1])
}

func TestRunDoesNotFormatAStateDiskThatHasAFileSystem(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, formatted: true}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.NotContains(t, sys.calls, "format /dev/vdb")
	assert.Contains(t, sys.calls, "mount /dev/vdb /var/lib/aibox/state")
}

func TestRunPowersOffWhenTheStateDiskCannotBeFormatted(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failFormat: errors.New("no mke2fs")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "no mke2fs")
	assert.Contains(t, sys.consoleOutput(), "no mke2fs")
	assert.NotContains(t, sys.calls, "start /usr/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunMountsTheSharesOfTheHostAfterItsOwn(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.mount=mount0:/opt/go aibox.mount=mount1:/opt/bin"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)

	last := slices.Index(sys.calls, "own /usr/local/bin")
	require.NotEqual(t, -1, last)

	next := sys.calls[last+1:]
	assert.Equal(t, []string{"mount mount0 /opt/go", "mount mount1 /opt/bin"}, next[:2])
}

func TestRunMountsTheSharesOfTheHostReadOnlyButExecutable(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.mount=mount0:/opt/go"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Equal(t, mounted{"mount0", "/opt/go", "virtiofs", syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV, ""}, sys.mounts["/opt/go"])
}

func TestRunPowersOffWhenAMountOfTheHostFails(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.mount=mount0:/opt/go", failMount: "mount0"}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "/opt/go")
	assert.Contains(t, sys.consoleOutput(), "/opt/go")
	assert.NotContains(t, sys.calls, "start /usr/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
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
	assert.NotContains(t, sys.calls, "start /usr/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunPowersOffWhenAnEarlyMountFails(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failMount: "devtmpfs"}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "devtmpfs")
	assert.Equal(t, append(slices.Clone(overlayCalls), "mount devtmpfs /dev", "halt"), sys.calls)
}

func TestRunPowersOffWithoutAConsole(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failOpen: errors.New("no such device")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "no such device")
	assert.NotContains(t, sys.calls, "start /usr/bin/claude")
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
	assert.Contains(t, sys.calls, "start /usr/bin/claude")
}

func TestRunHandsTheExitCodeToTheHostAndPowersOff(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, exitCode: 7}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Equal(t, 7, sys.exitCodeOnTheHost(t))
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunGivesTheCommandTheTerminalOfTheHost(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, realCommand: "stty size; echo TERM=$TERM; tty"}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Equal(t, 0, sys.exitCodeOnTheHost(t))
	assert.Contains(t, sys.screen.String(), "50 160")
	assert.Contains(t, sys.screen.String(), "TERM=xterm-kitty")
	assert.Contains(t, sys.screen.String(), "/dev/pts/")
}

func TestRunGivesAHostWithoutATerminalThePipesOfTheCommand(t *testing.T) {
	// arrange
	sys := &fakeSystem{
		t:           t,
		noTerminal:  true,
		input:       "the task\n",
		realCommand: `read line; echo "got $line"; echo TERM=$TERM; echo warning >&2; tty || true; exit 4`,
	}

	// act
	err := guest.Run(sys)

	// assert
	require.NoError(t, err)
	assert.Equal(t, 4, sys.exitCodeOnTheHost(t))
	assert.Contains(t, sys.screen.String(), "got the task")
	assert.Contains(t, sys.screen.String(), "TERM=dumb")
	assert.Contains(t, sys.screen.String(), "not a tty")
	assert.Equal(t, "warning\n", sys.errScreen.String())
}

func TestCommandWithoutATerminalLeavesTheStandardFilesToTheCaller(t *testing.T) {
	// act
	cmd := guest.Command(guest.Options{}, nil, session.Request{Env: []string{"GOFLAGS=-mod=mod"}})

	// assert
	assert.Nil(t, cmd.Stdin)
	assert.Nil(t, cmd.Stdout)
	assert.Nil(t, cmd.Stderr)
	assert.False(t, cmd.SysProcAttr.Setctty)
	assert.True(t, cmd.SysProcAttr.Setsid)
	assert.Contains(t, cmd.Env, "TERM=dumb")
	assert.Contains(t, cmd.Env, "GOFLAGS=-mod=mod")
}

func TestRunPowersOffWithoutATerminalPort(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: "console=hvc0"}

	// act
	err := guest.Run(sys)

	// assert
	require.ErrorIs(t, err, guest.ErrNoTerminal)
	assert.Contains(t, sys.consoleOutput(), "aibox.terminal")
	assert.NotContains(t, sys.calls, "start /usr/bin/claude")
	assert.Equal(t, "halt", lastCall(t, sys))
}

func TestRunPowersOffWhenTheHostDoesNotAnswer(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failDial: errors.New("connection reset")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "connection reset")
	assert.Contains(t, sys.consoleOutput(), "connection reset")
	assert.NotContains(t, sys.calls, "start /usr/bin/claude")
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

// fakeSystem stands in for the kernel. It also plays the host on the other
// end of the terminal port: a session client whose screen collects what the
// command prints. realCommand, when set, runs in place of the command with
// its terminal, so that a test can look at the terminal from inside.
// overlayCalls is how every boot starts: the overlay over the read-only
// root becomes the root before anything else is mounted.
var overlayCalls = []string{
	"mount tmpfs /run",
	"mkdir /run/upper",
	"mkdir /run/work",
	"mount overlay /run/root",
	"pivot /run/root /mnt",
}

type fakeSystem struct {
	t       *testing.T
	cmdline string
	calls   []string
	// mounts is the last mount on each target, mountsOf all of them
	mounts   map[string]mounted
	mountsOf map[string][]mounted
	tty      *os.File
	exitCode int
	orphans  int
	waits    int
	child    int
	listener net.Listener
	mu       sync.Mutex
	screen   syncBuffer
	// noTerminal makes the host a client without a terminal, which sends
	// input and collects standard error in errScreen
	noTerminal  bool
	input       string
	errScreen   syncBuffer
	attached    chan attachResult
	realCommand string
	real        *exec.Cmd
	// clientEnv is what the session client on the host sends
	clientEnv []string
	// started is the last command Start was given
	started    *exec.Cmd
	formatted  bool
	failFormat error
	failMount  string
	failPivot  bool
	failChmod  error
	// failDelegate is the error of every Delegate, startedIn the cgroup
	// the command was started in
	failDelegate error
	startedIn    string
	// files are the paths that exist
	files []string
	// listeners are the listeners of Listen by the address asked for
	listeners map[string]net.Listener
	// proxy answers a DialHost of proxyPort, the proxy on the host
	proxyPort    uint32
	proxy        func() (net.Conn, error)
	failLoopback error
	failListen   error
	failOpen     error
	failHostname error
	failDial     error
	failStart    error
	failWait     error
	failHalt     error
	// stderr is what the init wrote to its standard error
	stderr syncBuffer
}

type attachResult struct {
	code int
	err  error
}

type syncBuffer struct {
	mu     sync.Mutex
	buffer strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

// exitCodeOnTheHost is what the session client on the host came back with.
func (s *fakeSystem) exitCodeOnTheHost(t *testing.T) int {
	t.Helper()
	require.NotNil(t, s.attached, "the host was never dialed")

	select {
	case result := <-s.attached:
		require.NoError(t, result.err)

		return result.code
	case <-time.After(5 * time.Second):
		t.Fatal("the session on the host did not end")

		return 0
	}
}

func (s *fakeSystem) Mount(source, target, fstype string, flags uintptr, data string) error {
	s.record("mount " + source + " " + target)

	if s.mounts == nil {
		s.mounts = map[string]mounted{}
		s.mountsOf = map[string][]mounted{}
	}

	s.mounts[target] = mounted{source, target, fstype, flags, data}
	s.mountsOf[target] = append(s.mountsOf[target], s.mounts[target])

	if source == s.failMount {
		return errors.New("no such device")
	}

	return nil
}

func (s *fakeSystem) Blank(device string) (bool, error) {
	s.record("blank " + device)

	return !s.formatted, nil
}

func (s *fakeSystem) Format(device string) error {
	s.record("format " + device)

	return s.failFormat
}

func (s *fakeSystem) Own(path string) error {
	s.record("own " + path)

	return nil
}

func (s *fakeSystem) Chmod(path string, mode os.FileMode) error {
	s.record(fmt.Sprintf("chmod %o %s", mode, path))

	if err := s.exists(path); err != nil {
		return err
	}

	return s.failChmod
}

// exists is the error a change of the file would give.
func (s *fakeSystem) exists(path string) error {
	if !slices.Contains(s.files, path) {
		return fs.ErrNotExist
	}

	return nil
}

func (s *fakeSystem) Mkdir(path string) error {
	s.record("mkdir " + path)
	s.files = append(s.files, path)

	return nil
}

func (s *fakeSystem) PivotRoot(newRoot, putOld string) error {
	s.record("pivot " + newRoot + " " + putOld)

	if s.failPivot {
		return errors.New("invalid argument")
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
		return withTerminal, nil
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

	s.mu.Lock()
	if s.listeners == nil {
		s.listeners = map[string]net.Listener{}
	}

	s.listeners[address] = listener
	s.mu.Unlock()

	return listener, nil
}

func (s *fakeSystem) DialHost(port uint32) (net.Conn, error) {
	s.record(fmt.Sprintf("dial host %d", port))

	if s.proxy != nil && port == s.proxyPort {
		return s.proxy()
	}

	if port != terminalPort {
		return nil, errors.New("no host in the test")
	}

	if s.failDial != nil {
		return nil, s.failDial
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(s.t, err)

	defer func() { _ = listener.Close() }()

	s.attached = make(chan attachResult, 1)

	go func() {
		hostSide, err := listener.Accept()
		if err != nil {
			s.attached <- attachResult{err: err}

			return
		}

		defer func() { _ = hostSide.Close() }()

		if s.noTerminal {
			client := session.Exec{In: strings.NewReader(s.input), Out: &s.screen, Errors: &s.errScreen, Env: s.clientEnv}
			code, err := client.Run(hostSide)
			s.attached <- attachResult{code: code, err: err}

			return
		}

		client := session.Client{In: strings.NewReader(""), Out: &s.screen, Term: "xterm-kitty", Size: session.Size{Rows: 50, Cols: 160}, Env: s.clientEnv}
		code, err := client.Attach(hostSide)
		s.attached <- attachResult{code: code, err: err}
	}()

	guestSide, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = guestSide.Close() })

	return guestSide, nil
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

func (s *fakeSystem) Controllers(cgroup string) error {
	s.record("controllers " + cgroup)

	return nil
}

func (s *fakeSystem) Delegate(cgroup string) error {
	s.record("delegate " + cgroup)

	return s.failDelegate
}

func (s *fakeSystem) Start(cmd *exec.Cmd, cgroup string) (int, error) {
	s.record("start " + cmd.Path)
	s.started = cmd
	s.startedIn = cgroup

	if s.failStart != nil {
		return 0, s.failStart
	}

	s.child = 4242

	if s.realCommand == "" {
		return s.child, nil
	}

	// the test does not run as root, so it cannot become the VM user
	s.real = exec.Command("/bin/sh", "-c", s.realCommand) //nolint:gosec // the command comes from the test
	s.real.Env = cmd.Env
	s.real.Stdin, s.real.Stdout, s.real.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	s.real.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: cmd.SysProcAttr.Setctty, Ctty: cmd.SysProcAttr.Ctty}

	if err := s.real.Start(); err != nil {
		return 0, err
	}

	s.child = s.real.Process.Pid

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

	if s.real != nil {
		err := s.real.Wait()

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return s.child, exitErr.ExitCode(), nil
		}

		return s.child, 0, err
	}

	return s.child, s.exitCode, nil
}

func (s *fakeSystem) Stderr() io.Writer { return &s.stderr }

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
