//go:build linux

// Package guest is the first process of the VM. It mounts what Claude Code
// needs, starts it on the console and powers the VM off when it exits.
package guest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const (
	hostname = "aibox"
	project  = "/project"
	home     = "/home/user"
	claude   = "/usr/local/bin/claude"
	bash     = "/usr/bin/bash"
	userName = "user"

	// the user of image/passwd, with the ID the project files have on the
	// host
	uid uint32 = 1000
	gid uint32 = 1000

	// the tags of the shares, as the host names them
	projectShare = "project"
	homeShare    = "home"
)

// Options come from the kernel command line.
type Options struct {
	Console string
	Shell   bool
}

// System is what Run needs from the kernel.
type System interface {
	Mount(source, target, fstype string, flags uintptr, data string) error
	Symlink(target, path string) error
	ReadCmdline() (string, error)
	OpenConsole(path string) (*os.File, error)
	Sethostname(name string) error
	Start(cmd *exec.Cmd) (pid int, err error)
	Wait() (pid, exitCode int, err error)
	Poweroff() error
}

type mount struct {
	source, target, fstype string
	flags                  uintptr
	data                   string
}

type link struct {
	target, path string
}

const noDevices = syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV

var (
	// the console lives in /dev and its name is in /proc, so these two come
	// before everything else
	earlyMounts = []mount{
		{source: "devtmpfs", target: "/dev", fstype: "devtmpfs", flags: syscall.MS_NOSUID, data: "mode=755"},
		{source: "proc", target: "/proc", fstype: "proc", flags: noDevices},
	}

	mounts = []mount{
		{source: "sysfs", target: "/sys", fstype: "sysfs", flags: noDevices},
		{source: "devpts", target: "/dev/pts", fstype: "devpts", flags: syscall.MS_NOSUID | syscall.MS_NOEXEC, data: "mode=620,ptmxmode=666,gid=5"},
		{source: "tmpfs", target: "/dev/shm", fstype: "tmpfs", flags: syscall.MS_NOSUID | syscall.MS_NODEV, data: "mode=1777"},
		{source: "tmpfs", target: "/tmp", fstype: "tmpfs", flags: syscall.MS_NOSUID | syscall.MS_NODEV, data: "mode=1777"},
		{source: "tmpfs", target: "/run", fstype: "tmpfs", flags: syscall.MS_NOSUID | syscall.MS_NODEV, data: "mode=755"},
		{source: projectShare, target: project, fstype: "virtiofs"},
		{source: homeShare, target: home, fstype: "virtiofs"},
	}

	// devtmpfs does not create these
	links = []link{
		{target: "/proc/self/fd", path: "/dev/fd"},
		{target: "/proc/self/fd/0", path: "/dev/stdin"},
		{target: "/proc/self/fd/1", path: "/dev/stdout"},
		{target: "/proc/self/fd/2", path: "/dev/stderr"},
	}
)

// ParseCmdline reads the options from the kernel command line. The last
// console= word wins.
func ParseCmdline(cmdline string) Options {
	options := Options{Console: "/dev/console"}

	for _, word := range strings.Fields(cmdline) {
		key, value, _ := strings.Cut(word, "=")

		switch key {
		case "console":
			if name, _, _ := strings.Cut(value, ","); name != "" {
				options.Console = "/dev/" + name
			}
		case "aibox.shell":
			options.Shell = true
		}
	}

	return options
}

// Command is Claude Code, or a shell when the options ask for one, set up to
// run as the user on the console.
func Command(options Options, console *os.File) *exec.Cmd {
	cmd := exec.Command(claude)
	if options.Shell {
		cmd = exec.Command(bash, "-l")
	}

	cmd.Dir = project
	cmd.Env = []string{
		"HOME=" + home,
		"USER=" + userName,
		"LOGNAME=" + userName,
		"SHELL=" + bash,
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
	}
	cmd.Stdin = console
	cmd.Stdout = console
	cmd.Stderr = console
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uid, Gid: gid},
		Setsid:     true,
		Setctty:    true,
		Ctty:       0,
	}

	return cmd
}

// Run sets the VM up, runs the command until it exits and powers off. The
// error says what went wrong before the VM powered off.
func Run(sys System) error {
	console, options, err := setup(sys)
	if err == nil {
		err = supervise(sys, Command(options, console), console)
	}

	if err != nil && console != nil {
		say(console, "aibox: %v\n", err)
	}

	if offErr := sys.Poweroff(); offErr != nil {
		return errors.Join(err, fmt.Errorf("power off: %w", offErr))
	}

	return err
}

func setup(sys System) (*os.File, Options, error) {
	for _, m := range earlyMounts {
		if err := sys.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			return nil, Options{}, fmt.Errorf("mount %s on %s: %w", m.source, m.target, err)
		}
	}

	cmdline, err := sys.ReadCmdline()
	if err != nil {
		return nil, Options{}, fmt.Errorf("read the kernel command line: %w", err)
	}

	options := ParseCmdline(cmdline)

	console, err := sys.OpenConsole(options.Console)
	if err != nil {
		return nil, options, fmt.Errorf("open the console %s: %w", options.Console, err)
	}

	for _, m := range mounts {
		if err := sys.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			return console, options, fmt.Errorf("mount %s on %s: %w", m.source, m.target, err)
		}
	}

	for _, l := range links {
		if err := sys.Symlink(l.target, l.path); err != nil {
			say(console, "aibox: link %s: %v\n", l.path, err)
		}
	}

	if err := sys.Sethostname(hostname); err != nil {
		say(console, "aibox: set the hostname: %v\n", err)
	}

	return console, options, nil
}

// supervise runs the command and reaps every child until the command
// exits. As PID 1 the init also inherits the children whose parents are
// gone.
func supervise(sys System, cmd *exec.Cmd, console io.Writer) error {
	pid, err := sys.Start(cmd)
	if err != nil {
		return fmt.Errorf("start %s: %w", cmd.Path, err)
	}

	for {
		exited, exitCode, err := sys.Wait()
		if err != nil {
			return fmt.Errorf("wait for %s: %w", cmd.Path, err)
		}

		if exited != pid {
			continue
		}

		if exitCode != 0 {
			say(console, "aibox: %s ended with exit code %d\n", cmd.Path, exitCode)
		}

		return nil
	}
}

func say(console io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(console, format, args...)
}
