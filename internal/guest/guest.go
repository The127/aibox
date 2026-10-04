//go:build linux

// Package guest is the first process of the VM. It mounts what Claude Code
// needs, starts it on the console and powers the VM off when it exits.
package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/the127/aibox/internal/tunnel"
	"github.com/the127/aibox/internal/vm"
)

const (
	hostname = "aibox"
	project  = "/project"
	home     = "/home/user"
	claude   = "/usr/local/bin/claude"
	bash     = "/usr/bin/bash"
	userName = "user"

	// the tags of the shares, as the host names them
	projectShare = "project"
	homeShare    = "home"

	// where Claude Code finds the proxy inside the VM
	guestProxyAddress = "127.0.0.1:3128"
)

// ErrBadProxyPort is a kernel command line whose aibox.proxy word is not a
// vsock port.
var ErrBadProxyPort = errors.New("aibox.proxy is not a port")

// Options come from the kernel command line.
type Options struct {
	Console   string
	Shell     bool
	ProxyPort uint32
}

// Network is what the proxy side of the VM needs from the kernel.
type Network interface {
	BringLoopbackUp() error
	Listen(address string) (net.Listener, error)
	DialHost(port uint32) (net.Conn, error)
}

// System is what Run needs from the kernel.
type System interface {
	Network
	Mount(source, target, fstype string, flags uintptr, data string) error
	Symlink(target, path string) error
	ReadCmdline() (string, error)
	OpenConsole(path string) (*os.File, error)
	Sethostname(name string) error
	Start(cmd *exec.Cmd) (pid int, err error)
	Wait() (pid, exitCode int, err error)
	Halt() error
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
// console= word wins. A word that cannot be read is left out of the options
// and reported in the error.
func ParseCmdline(cmdline string) (Options, error) {
	options := Options{Console: "/dev/console"}

	var err error

	for _, word := range strings.Fields(cmdline) {
		key, value, _ := strings.Cut(word, "=")

		switch key {
		case "console":
			if name, _, _ := strings.Cut(value, ","); name != "" {
				options.Console = "/dev/" + name
			}
		case "aibox.shell":
			options.Shell = true
		case "aibox.proxy":
			options.ProxyPort, err = parsePort(value)
		}
	}

	return options, err
}

// parsePort reads a vsock port. 0 and the highest value are not ports a
// listener can have.
func parsePort(value string) (uint32, error) {
	port, err := strconv.ParseUint(value, 10, 32)
	if err != nil || port == 0 || port == 0xFFFFFFFF {
		return 0, fmt.Errorf("%w: %q", ErrBadProxyPort, value)
	}

	return uint32(port), nil
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
		// an update would land in the home share and never run, because the
		// image's binary comes first on PATH
		"DISABLE_AUTOUPDATER=1",
	}

	// the proxy speaks CONNECT only, which is how HTTPS goes through a proxy.
	// Tools like curl read the lower case names.
	if options.ProxyPort != 0 {
		cmd.Env = append(cmd.Env,
			"HTTPS_PROXY=http://"+guestProxyAddress,
			"https_proxy=http://"+guestProxyAddress,
			"NO_PROXY=localhost,127.0.0.1",
			"no_proxy=localhost,127.0.0.1",
		)
	}

	cmd.Stdin = console
	cmd.Stdout = console
	cmd.Stderr = console
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: vm.GuestUID, Gid: vm.GuestGID},
		Setsid:     true,
		Setctty:    true,
		Ctty:       0,
	}

	return cmd
}

// Forward joins each client of the listener with a connection from dial. A
// client whose dial fails gets a 502 and the failure goes to the log. Forward
// returns when the context ends.
func Forward(ctx context.Context, listener net.Listener, dial func() (net.Conn, error), log io.Writer) error {
	return tunnel.Serve(ctx, listener, func(ctx context.Context, client net.Conn) {
		host, err := dial()
		if err != nil {
			say(log, "aibox: connect to the proxy on the host: %v\n", err)
			_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")

			return
		}

		defer func() { _ = host.Close() }()

		stop := context.AfterFunc(ctx, func() { _ = host.Close() })
		defer stop()

		tunnel.Join(client, host)
	})
}

// Run sets the VM up, runs the command until it exits and halts the VM. The
// error says what went wrong before the halt.
func Run(sys System) error {
	console, options, err := setup(sys)
	if err == nil {
		err = supervise(sys, Command(options, console), console)
	}

	if err != nil && console != nil {
		say(console, "aibox: %v\n", err)
	}

	if haltErr := sys.Halt(); haltErr != nil {
		return errors.Join(err, fmt.Errorf("halt: %w", haltErr))
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

	options, badWord := ParseCmdline(cmdline)

	console, err := sys.OpenConsole(options.Console)
	if err != nil {
		return nil, options, fmt.Errorf("open the console %s: %w", options.Console, err)
	}

	if badWord != nil {
		say(console, "aibox: %v\n", badWord)
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

	if options.ProxyPort != 0 {
		if err := startProxy(sys, options.ProxyPort, console); err != nil {
			return console, options, err
		}
	}

	return console, options, nil
}

// startProxy listens on the proxy address of the VM and forwards each
// connection to the proxy on the host over vsock.
func startProxy(network Network, port uint32, console io.Writer) error {
	if err := network.BringLoopbackUp(); err != nil {
		return fmt.Errorf("bring the loopback interface up: %w", err)
	}

	listener, err := network.Listen(guestProxyAddress)
	if err != nil {
		return fmt.Errorf("listen for the proxy on %s: %w", guestProxyAddress, err)
	}

	dial := func() (net.Conn, error) { return network.DialHost(port) }

	// the forwarder lives as long as the VM
	go func() {
		if err := Forward(context.Background(), listener, dial, console); err != nil {
			say(console, "aibox: the proxy forwarder stopped: %v\n", err)
		}
	}()

	return nil
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
