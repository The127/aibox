//go:build linux

package guest_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/guest"
)

// untilReleased is a command that runs until the test creates the file it
// returns, so that the command outlives the checks of the test without a
// guess at how long they take.
func untilReleased(t *testing.T) (command, release string) {
	t.Helper()

	release = filepath.Join(t.TempDir(), "release")

	return "while [ ! -e " + release + " ]; do sleep 0.01; done", release
}

// containerCmdline is what the kernel of a container VM starts with, plus
// the words the host adds.
const containerCmdline = "console=hvc0 tsc=reliable panic=0 init=/sbin/vminitd ro root=/dev/vda"

func TestParseCmdlineTakesABareProxyWordForAProxyOverTheLink(t *testing.T) {
	// act
	options, err := guest.ParseCmdline("aibox.proxy")

	// assert
	require.NoError(t, err)
	assert.Equal(t, guest.Options{Console: "/dev/console", Proxy: true}, options)
}

func TestRunInAContainerUsesWhatTheRuntimeMountedAndServesOverTheSocket(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: containerCmdline}

	// act
	err := guest.Run(sys, &guest.Container{})

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"mkdir /var/lib/aibox",
		"listen socket /var/lib/aibox/link.sock",
		"mount proc /proc",
		"read cmdline",
		"open /proc/self/fd/1",
		"mount devpts /dev/pts",
		"mount tmpfs /dev/shm",
		"mount tmpfs /tmp",
		"mount tmpfs /var/tmp",
		"mount tmpfs /run",
		"enter /sys/fs/cgroup/init",
		"controllers /sys/fs/cgroup",
		"delegate /sys/fs/cgroup/user",
		"controllers /sys/fs/cgroup/user",
		"delegate /sys/fs/cgroup/user/session",
		"chmod 666 /dev/kvm",
		"chmod 666 /dev/fuse",
		"pin /project/.git",
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
		"start /usr/bin/claude",
		"wait",
		"halt",
	}, sys.callsCopy())
	assert.Equal(t, 0, sys.exitCodeOnTheHost(t))
}

func TestRunInAContainerHandsTheExitCodeToTheHost(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: containerCmdline, realCommand: "exit 3"}

	// act
	err := guest.Run(sys, &guest.Container{})

	// assert
	require.NoError(t, err)
	assert.Equal(t, 3, sys.exitCodeOnTheHost(t))
}

func TestRunInAContainerForwardsTheProxyOverTheLink(t *testing.T) {
	// arrange
	command, release := untilReleased(t)
	sys := &fakeSystem{t: t, cmdline: containerCmdline + " aibox.proxy", realCommand: command}
	ran := make(chan error, 1)

	// act
	go func() { ran <- guest.Run(sys, &guest.Container{}) }()

	// assert
	require.Eventually(t, func() bool { return slices.Contains(sys.callsCopy(), "start /usr/bin/claude") }, 5*time.Second, 10*time.Millisecond)

	client := dialWithDeadline(t, sys.listener.Addr().String())
	answer, err := io.ReadAll(client)
	require.NoError(t, err)
	assert.Equal(t, proxyAnswer, string(answer))
	assert.Contains(t, sys.callsCopy(), "listen 127.0.0.1:3128")
	require.NoError(t, os.WriteFile(release, nil, 0o600))
	require.NoError(t, <-ran)
}

func TestRunInAContainerPowersOffWhenItCannotListenForTheHost(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: containerCmdline, failSocket: errors.New("read-only file system")}

	// act
	err := guest.Run(sys, &guest.Container{})

	// assert
	require.ErrorContains(t, err, "read-only file system")
	assert.Equal(t, "halt", sys.callsCopy()[len(sys.callsCopy())-1])
}

func TestRunInAContainerPowersOffWhenTheInitCannotLeaveTheRootCgroup(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, cmdline: containerCmdline, failEnter: errors.New("device busy")}

	// act
	err := guest.Run(sys, &guest.Container{})

	// assert
	require.ErrorContains(t, err, "device busy")
	assert.True(t, strings.Contains(sys.consoleOutput(), "device busy"))
	assert.NotContains(t, sys.callsCopy(), "controllers /sys/fs/cgroup")
	assert.Equal(t, "halt", sys.callsCopy()[len(sys.callsCopy())-1])
}

func TestPlatformOfAnInitWithoutArgumentsIsQEMU(t *testing.T) {
	// act
	platform, err := guest.PlatformOf(nil)

	// assert
	require.NoError(t, err)
	assert.Equal(t, guest.QEMU{}, platform)
}

func TestPlatformOfAnInitStartedForAContainerIsContainer(t *testing.T) {
	// act
	platform, err := guest.PlatformOf([]string{"container"})

	// assert
	require.NoError(t, err)
	assert.IsType(t, &guest.Container{}, platform)
}

func TestPlatformOfAnUnknownNameIsAnError(t *testing.T) {
	// act
	_, err := guest.PlatformOf([]string{"firecracker"})

	// assert
	require.ErrorIs(t, err, guest.ErrUnknownPlatform)
	assert.ErrorContains(t, err, "firecracker")
}

func TestRunUnderQEMUSaysWhyAProxyWithoutAPortCannotBeReached(t *testing.T) {
	// arrange
	command, release := untilReleased(t)
	sys := &fakeSystem{t: t, cmdline: withTerminal + " aibox.proxy", realCommand: command}
	ran := make(chan error, 1)

	go func() { ran <- guest.Run(sys, guest.QEMU{}) }()

	require.Eventually(t, func() bool { return slices.Contains(sys.callsCopy(), "start /usr/bin/claude") }, 5*time.Second, 10*time.Millisecond)

	// act
	client := dialWithDeadline(t, sys.listener.Addr().String())
	answer, err := io.ReadAll(client)

	// assert
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(answer), "HTTP/1.1 502"), string(answer))
	assert.Contains(t, sys.consoleOutput(), guest.ErrNoProxyPort.Error())
	assert.NotContains(t, sys.callsCopy(), "dial host 0")
	require.NoError(t, os.WriteFile(release, nil, 0o600))
	require.NoError(t, <-ran)
}

func TestAContainerThatWasNotPreparedCannotConnect(t *testing.T) {
	// act
	_, err := (&guest.Container{}).Connect(&fakeSystem{t: t}, guest.Options{})

	// assert
	assert.ErrorIs(t, err, guest.ErrNotPrepared)
}
