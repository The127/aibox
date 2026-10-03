//go:build linux

package guest_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

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
	}

	for cmdline, want := range tests {
		t.Run(cmdline, func(t *testing.T) {
			// act
			options := guest.ParseCmdline(cmdline)

			// assert
			assert.Equal(t, want, options)
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
		"poweroff",
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
	assert.Equal(t, "poweroff", lastCall(t, sys))
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
	assert.Equal(t, "poweroff", lastCall(t, sys))
}

func TestRunPowersOffWhenAnEarlyMountFails(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failMount: "devtmpfs"}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "devtmpfs")
	assert.Equal(t, []string{"mount devtmpfs /dev", "poweroff"}, sys.calls)
}

func TestRunPowersOffWithoutAConsole(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failOpen: errors.New("no such device")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "no such device")
	assert.NotContains(t, sys.calls, "start /usr/local/bin/claude")
	assert.Equal(t, "poweroff", lastCall(t, sys))
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
	assert.Equal(t, "poweroff", lastCall(t, sys))
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
	assert.Equal(t, "poweroff", lastCall(t, sys))
}

func TestRunPowersOffWhenWaitFails(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failWait: errors.New("no child processes")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "no child processes")
	assert.Equal(t, "poweroff", lastCall(t, sys))
}

func TestRunReportsAFailedPoweroff(t *testing.T) {
	// arrange
	sys := &fakeSystem{t: t, failPoweroff: errors.New("not permitted")}

	// act
	err := guest.Run(sys)

	// assert
	assert.ErrorContains(t, err, "power off")
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
	failMount    string
	failOpen     error
	failHostname error
	failStart    error
	failWait     error
	failPoweroff error
}

func (s *fakeSystem) Mount(source, target, fstype string, flags uintptr, data string) error {
	s.calls = append(s.calls, "mount "+source+" "+target)

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
	s.calls = append(s.calls, "link "+path+" -> "+target)

	return nil
}

func (s *fakeSystem) ReadCmdline() (string, error) {
	s.calls = append(s.calls, "read cmdline")

	if s.cmdline == "" {
		return "console=ttyS0", nil
	}

	return s.cmdline, nil
}

func (s *fakeSystem) OpenConsole(path string) (*os.File, error) {
	s.calls = append(s.calls, "open "+path)

	if s.failOpen != nil {
		return nil, s.failOpen
	}

	s.tty = newConsoleFile(s.t)

	return s.tty, nil
}

func (s *fakeSystem) Sethostname(name string) error {
	s.calls = append(s.calls, "hostname "+name)

	return s.failHostname
}

func (s *fakeSystem) Start(cmd *exec.Cmd) (int, error) {
	s.calls = append(s.calls, "start "+cmd.Path)

	if s.failStart != nil {
		return 0, s.failStart
	}

	s.child = 4242

	return s.child, nil
}

func (s *fakeSystem) Wait() (int, int, error) {
	s.calls = append(s.calls, "wait")
	s.waits++

	if s.failWait != nil {
		return 0, 0, s.failWait
	}

	if s.waits <= s.orphans {
		return 100 + s.waits, 0, nil
	}

	return s.child, s.exitCode, nil
}

func (s *fakeSystem) Poweroff() error {
	s.calls = append(s.calls, "poweroff")

	return s.failPoweroff
}

func (s *fakeSystem) consoleOutput() string {
	if s.tty == nil {
		return ""
	}

	content, err := os.ReadFile(s.tty.Name())
	require.NoError(s.t, err)

	return string(content)
}
