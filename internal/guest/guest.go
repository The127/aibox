//go:build linux

// Package guest is the first process of the VM. It mounts what Claude Code
// needs, runs it on a terminal served to the host and powers the VM off
// when it exits.
package guest

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
	"strconv"
	"strings"
	"syscall"

	"github.com/the127/aibox/internal/session"
	"github.com/the127/aibox/internal/tunnel"
	"github.com/the127/aibox/internal/vm"
)

const (
	hostname = "aibox"
	project  = "/project"
	home     = "/home/user"
	claude   = "/usr/bin/claude"
	prompt   = "/etc/aibox/prompt.md"
	bash     = "/usr/bin/bash"
	userName = "user"

	// the second disk is mounted outside the folders anyone looks at, and
	// the folders on it are bound where tools and caches land
	stateDevice = "/dev/vdb"
	stateMount  = "/var/lib/aibox/state"

	// the root disk is read-only, so the mount points are made on an
	// overlay in RAM. It is built under /run, which is empty in the image
	// and gets a tmpfs of its own after the pivot.
	overlayDir  = "/run"
	overlayRoot = overlayDir + "/root"
	overlayData = "lowerdir=/,upperdir=" + overlayDir + "/upper,workdir=" + overlayDir + "/work"
	oldRoot     = "/mnt"

	// the tags of the shares, as the host names them
	projectShare = "project"
	homeShare    = "home"

	// where Claude Code finds the proxy inside the VM
	guestProxyAddress = "127.0.0.1:3128"
)

var (
	// ErrBadPort is a kernel command line whose aibox.proxy or
	// aibox.terminal word is not a vsock port.
	ErrBadPort = errors.New("not a vsock port")
	// ErrNoTerminal is a kernel command line without an aibox.terminal word.
	ErrNoTerminal = errors.New("no aibox.terminal on the kernel command line")
	// ErrBadMountWord is an aibox.mount word that is not a share tag and an
	// absolute path joined by a colon.
	ErrBadMountWord = errors.New("not tag:path with an absolute path")
)

// imagePath is where the programs of the image are. The PATH the host
// sends goes in front of it.
const imagePath = "/usr/local/bin:/usr/bin:/bin"

// Options come from the kernel command line. Mounts are the shares of the
// host that are mounted read-only where the host says.
type Options struct {
	Console      string
	Shell        bool
	ProxyPort    uint32
	TerminalPort uint32
	Mounts       []Mount
}

// Mount is a share of the host and the path the VM mounts it on.
type Mount struct {
	Tag  string
	Path string
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
	// Mkdir makes the folder and its parents, if missing.
	Mkdir(path string) error
	// Chmod sets the mode of the file. The error wraps fs.ErrNotExist when
	// there is no such file.
	Chmod(path string, mode os.FileMode) error
	// Pin makes the file or folder a mount point, so that it can neither
	// be renamed nor removed. The error wraps fs.ErrNotExist when the path
	// does not exist, and syscall.ENOTDIR when a folder on the way is a file.
	Pin(path string) error
	// Protect pins the file or folder and makes it read-only, with the
	// same errors.
	Protect(path string) error
	// PivotRoot makes newRoot the root and lets the old root go. putOld is
	// where the old root goes meanwhile, as a path inside the new root.
	PivotRoot(newRoot, putOld string) error
	// Blank tells whether the disk is empty. It returns ErrDamaged when the
	// disk has data but no ext4 file system.
	Blank(device string) (bool, error)
	// Format puts a file system on the disk.
	Format(device string) error
	// Own makes the folder, if missing, and gives it to the user.
	Own(path string) error
	Symlink(target, path string) error
	ReadCmdline() (string, error)
	OpenConsole(path string) (*os.File, error)
	Sethostname(name string) error
	// Start starts the command in the cgroup, a folder of the cgroup2 file
	// system.
	Start(cmd *exec.Cmd, cgroup string) (pid int, err error)
	// Controllers turns the cgroup controllers on for the cgroups below.
	Controllers(cgroup string) error
	// Delegate makes the cgroup, if missing, and gives it to the user, so
	// that the user can make cgroups below it.
	Delegate(cgroup string) error
	Wait() (pid, exitCode int, err error)
	Halt() error
	// Stderr is the standard error the init started with, the console of
	// the kernel.
	Stderr() io.Writer
}

type mount struct {
	source, target, fstype string
	flags                  uintptr
	data                   string
}

type link struct {
	target, path string
}

const (
	noDevices = syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV
	// programs in a mounted folder must run, so it stays executable
	readOnlyShare = syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV
	stateFlags    = syscall.MS_NOSUID | syscall.MS_NODEV
)

// ErrDamaged is returned for a state disk that has data but no ext4 file
// system, which the init will not format over.
var ErrDamaged = errors.New("has data but no ext4 file system, remove it to start over")

// gitDir is pinned by protectGit, and gitConfig and gitDirs made read-only.
// .git/info stays writable: lefthook keeps unstaged changes there during a
// commit, and exclude and attributes in it run nothing on the host.
const (
	gitDir    = project + "/.git"
	gitConfig = gitDir + "/config"
)

var gitDirs = []string{gitDir + "/hooks"}

// localBin is made on the state disk, cache is where caches go.
const (
	localBin = "/usr/local/bin"
	cache    = home + "/.cache"
)

// stateDirs are the folders of the state disk and where they are bound.
var stateDirs = []struct{ dir, target string }{
	{"local", "/usr/local"},
	{"cache", cache},
	// overlayfs does not work on top of virtio-fs, so container images need
	// a real disk
	{"containers", home + "/.local/share/containers"},
}

// The command runs in a cgroup of its own below one that belongs to the
// user, so that containers can have limits. A cgroup with processes in it
// cannot hand controllers down, which is why the command is one level
// further down.
const (
	cgroupRoot    = "/sys/fs/cgroup"
	userCgroup    = cgroupRoot + "/user"
	sessionCgroup = userCgroup + "/session"
)

// devices are opened to everyone for containers and VMs inside the VM. A
// missing one is skipped, since the kernel makes /dev/kvm only where the
// host allows nested virtualisation.
var devices = []string{"/dev/kvm", "/dev/fuse"}

var (
	// the console lives in /dev and its name is in /proc, so these two come
	// before everything else
	earlyMounts = []mount{
		{source: "devtmpfs", target: "/dev", fstype: "devtmpfs", flags: syscall.MS_NOSUID, data: "mode=755"},
		{source: "proc", target: "/proc", fstype: "proc", flags: noDevices},
	}

	mounts = []mount{
		{source: "sysfs", target: "/sys", fstype: "sysfs", flags: noDevices},
		{source: "cgroup2", target: "/sys/fs/cgroup", fstype: "cgroup2", flags: noDevices, data: "nsdelegate"},
		{source: "devpts", target: "/dev/pts", fstype: "devpts", flags: syscall.MS_NOSUID | syscall.MS_NOEXEC, data: "mode=620,ptmxmode=666,gid=5"},
		{source: "tmpfs", target: "/dev/shm", fstype: "tmpfs", flags: syscall.MS_NOSUID | syscall.MS_NODEV, data: "mode=1777"},
		{source: "tmpfs", target: "/tmp", fstype: "tmpfs", flags: syscall.MS_NOSUID | syscall.MS_NODEV, data: "mode=1777"},
		{source: "tmpfs", target: "/var/tmp", fstype: "tmpfs", flags: syscall.MS_NOSUID | syscall.MS_NODEV, data: "mode=1777"},
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

	var errs []error

	port := func(key, value string) uint32 {
		port, err := parsePort(key, value)
		if err != nil {
			errs = append(errs, err)
		}

		return port
	}

	mount := func(value string) {
		mount, err := parseMount(value)
		if err != nil {
			errs = append(errs, err)

			return
		}

		options.Mounts = append(options.Mounts, mount)
	}

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
			options.ProxyPort = port(key, value)
		case "aibox.terminal":
			options.TerminalPort = port(key, value)
		case "aibox.mount":
			mount(value)
		}
	}

	return options, errors.Join(errs...)
}

// parseMount reads a tag:path word. The host checked the path already, but
// the kernel command line is input like any other.
func parseMount(value string) (Mount, error) {
	tag, path, _ := strings.Cut(value, ":")
	if tag == "" || strings.Count(value, ":") != 1 || !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
		return Mount{}, fmt.Errorf("aibox.mount=%q: %w", value, ErrBadMountWord)
	}

	return Mount{Tag: tag, Path: filepath.Clean(path)}, nil
}

// parsePort reads a vsock port. 0 and the highest value are not ports a
// listener can have.
func parsePort(key, value string) (uint32, error) {
	port, err := strconv.ParseUint(value, 10, 32)
	if err != nil || port == 0 || port == 0xFFFFFFFF {
		return 0, fmt.Errorf("%s=%q: %w", key, value, ErrBadPort)
	}

	return uint32(port), nil
}

// Command is Claude Code, or a shell when the options ask for one, set up to
// run as the user on the terminal with the TERM and the variables of the
// request. The folders of a PATH in the request go in front of the PATH of
// the image. The other variables aibox sets itself keep their values.
func Command(options Options, terminal *os.File, request session.Request) *exec.Cmd {
	cmd := exec.Command(claude, "--append-system-prompt-file", prompt)
	if options.Shell {
		cmd = exec.Command(bash, "-l")
	}

	hostPath, requested := takeVariable(request.Env, "PATH")
	own := ownVariables(options, request.Term, hostPath)
	accepted, _ := splitVariables(own, requested)

	cmd.Dir = project
	cmd.Env = slices.Concat(own, accepted)
	cmd.Stdin = terminal
	cmd.Stdout = terminal
	cmd.Stderr = terminal
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: vm.GuestUID, Gid: vm.GuestGID},
		Setsid:     true,
		Setctty:    true,
		Ctty:       0,
	}

	return cmd
}

// refused are the names of the variables of the request that aibox sets
// itself and keeps.
func refused(options Options, request session.Request) []string {
	_, requested := takeVariable(request.Env, "PATH")
	_, rejected := splitVariables(ownVariables(options, request.Term, ""), requested)

	return rejected
}

// takeVariable returns the value of the variable in the list and the list
// without it.
func takeVariable(env []string, name string) (string, []string) {
	var (
		value string
		rest  []string
	)

	for _, variable := range env {
		if v, ok := strings.CutPrefix(variable, name+"="); ok {
			value = v
		} else {
			rest = append(rest, variable)
		}
	}

	return value, rest
}

// ownVariables are the variables aibox sets for the command, with the
// folders of the host in front of the PATH. A folder that is not absolute
// is left out, like the host does, in case another client sends one.
func ownVariables(options Options, term, hostPath string) []string {
	path := imagePath

	if folders := slices.DeleteFunc(strings.Split(hostPath, ":"), notAFolder); len(folders) > 0 {
		path = strings.Join(folders, ":") + ":" + imagePath
	}

	env := []string{
		"AIBOX=1",
		"HOME=" + home,
		"USER=" + userName,
		"LOGNAME=" + userName,
		"SHELL=" + bash,
		"PATH=" + path,
		"TERM=" + term,
		"LANG=C.UTF-8",
		// an update would land in the home share and never run, because the
		// image's binary comes first on PATH. Telemetry and error reports go
		// to hosts the allow list does not have.
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		// thousands of small files, which the home share is slow for
		"GOMODCACHE=" + cache + "/go-mod",
	}

	// the proxy speaks CONNECT only, which is how HTTPS goes through a proxy.
	// Tools like curl read the lower case names.
	if options.ProxyPort != 0 {
		env = append(env,
			"HTTPS_PROXY=http://"+guestProxyAddress,
			"https_proxy=http://"+guestProxyAddress,
			"NO_PROXY=localhost,127.0.0.1",
			"no_proxy=localhost,127.0.0.1",
		)
	}

	return env
}

func notAFolder(folder string) bool {
	return !filepath.IsAbs(folder) || filepath.Clean(folder) == "/"
}

// splitVariables sorts the variables of the host into the ones to take and
// the names of the ones aibox sets itself. The host refuses those names in
// the config already, so this only guards against another client.
func splitVariables(own, requested []string) (accepted, rejected []string) {
	names := make(map[string]bool, len(own))

	for _, variable := range own {
		name, _, _ := strings.Cut(variable, "=")
		names[name] = true
	}

	for _, variable := range requested {
		name, _, _ := strings.Cut(variable, "=")
		if names[name] {
			rejected = append(rejected, name)
		} else {
			accepted = append(accepted, variable)
		}
	}

	return accepted, rejected
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

// Run sets the VM up, serves the terminal session to the host until the
// command exits and halts the VM. The error says what went wrong before the
// halt.
func Run(sys System) error {
	console, options, err := setup(sys)
	if err == nil {
		err = serve(sys, options, console)
	}

	// the halt ends the init before it can return, so an error from before
	// the console was open goes out here or not at all
	switch {
	case err != nil && console != nil:
		say(console, "aibox: %v\n", err)
	case err != nil:
		say(sys.Stderr(), "aibox: %v\n", err)
	}

	if haltErr := sys.Halt(); haltErr != nil {
		return errors.Join(err, fmt.Errorf("halt: %w", haltErr))
	}

	return err
}

func setup(sys System) (*os.File, Options, error) {
	if err := enterOverlay(sys); err != nil {
		return nil, Options{}, err
	}

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

	if err := delegateCgroups(sys); err != nil {
		return console, options, err
	}

	for _, device := range devices {
		if err := sys.Chmod(device, 0o666); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return console, options, fmt.Errorf("open %s to everyone: %w", device, err)
		}
	}

	if err := protectGit(sys); err != nil {
		return console, options, err
	}

	if err := mountState(sys); err != nil {
		return console, options, err
	}

	for _, m := range options.Mounts {
		if err := sys.Mount(m.Tag, m.Path, "virtiofs", readOnlyShare, ""); err != nil {
			return console, options, fmt.Errorf("mount %s on %s: %w", m.Tag, m.Path, err)
		}
	}

	if err := shareMounts(sys); err != nil {
		return console, options, err
	}

	if err := lockRoot(sys); err != nil {
		return console, options, err
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

// protectGit makes the config and hooks of the project's .git read-only,
// because a config key or a hook written there in the VM would run on the
// host the next time the person uses git, and neither git status nor git
// diff would show it. The .git folder itself is pinned, or it could be
// renamed away and made again without the mounts. A .git that is a file, as
// in a worktree or a submodule, names the real folder and is made read-only
// as a whole. A missing hooks folder is made first, since a folder made
// later in the VM would not be read-only.
func protectGit(sys System) error {
	err := sys.Pin(gitDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("pin %s: %w", gitDir, err)
	}

	err = sys.Protect(gitConfig)
	if errors.Is(err, syscall.ENOTDIR) {
		if err := sys.Protect(gitDir); err != nil {
			return fmt.Errorf("protect %s: %w", gitDir, err)
		}

		return nil
	}

	if err != nil {
		return fmt.Errorf("protect %s: %w", gitConfig, err)
	}

	for _, dir := range gitDirs {
		if err := protectOrMake(sys, dir); err != nil {
			return err
		}
	}

	return nil
}

// protectOrMake makes the folder read-only, making it first if missing.
func protectOrMake(sys System, dir string) error {
	err := sys.Protect(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := sys.Mkdir(dir); err != nil {
			return fmt.Errorf("make %s: %w", dir, err)
		}

		err = sys.Protect(dir)
	}

	if err != nil {
		return fmt.Errorf("protect %s: %w", dir, err)
	}

	return nil
}

// enterOverlay puts an overlay in RAM over the read-only root disk and
// makes it the root, so that the mount points of the shares can be made.
func enterOverlay(sys System) error {
	if err := sys.Mount("tmpfs", overlayDir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=755,size=16m"); err != nil {
		return fmt.Errorf("mount the overlay tmpfs: %w", err)
	}

	for _, dir := range []string{overlayDir + "/upper", overlayDir + "/work"} {
		if err := sys.Mkdir(dir); err != nil {
			return fmt.Errorf("make %s: %w", dir, err)
		}
	}

	if err := sys.Mount("overlay", overlayRoot, "overlay", 0, overlayData); err != nil {
		return fmt.Errorf("mount the overlay: %w", err)
	}

	if err := sys.PivotRoot(overlayRoot, oldRoot); err != nil {
		return fmt.Errorf("make %s the root: %w", overlayRoot, err)
	}

	return nil
}

// delegateCgroups gives the user a cgroup with every controller and puts
// the cgroup of the command below it.
func delegateCgroups(sys System) error {
	for _, step := range []struct {
		do     func(string) error
		cgroup string
	}{
		{sys.Controllers, cgroupRoot},
		{sys.Delegate, userCgroup},
		{sys.Controllers, userCgroup},
		{sys.Delegate, sessionCgroup},
	} {
		if err := step.do(step.cgroup); err != nil {
			return fmt.Errorf("set up the cgroup %s: %w", step.cgroup, err)
		}
	}

	return nil
}

// shareMounts makes every mount shared, which rootless containers need to
// propagate their mounts.
func shareMounts(sys System) error {
	if err := sys.Mount("shared", "/", "", syscall.MS_REC|syscall.MS_SHARED, ""); err != nil {
		return fmt.Errorf("share the mounts: %w", err)
	}

	return nil
}

// lockRoot makes the root read-only. Every mount point exists by now, and
// the folders that take writes are mounts of their own, so nothing needs
// the root writable any more.
func lockRoot(sys System) error {
	if err := sys.Mount("overlay", "/", "", syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("make the root read-only: %w", err)
	}

	return nil
}

// mountState mounts the state disk of the project, formatting it on the
// first boot, and binds its folders where tools and caches land.
func mountState(sys System) error {
	blank, err := sys.Blank(stateDevice)
	if err != nil {
		return fmt.Errorf("look at the state disk, state.ext4 of the project on the host: %w", err)
	}

	if blank {
		if err := sys.Format(stateDevice); err != nil {
			return fmt.Errorf("format the state disk: %w", err)
		}
	}

	if err := sys.Mount(stateDevice, stateMount, "ext4", stateFlags, ""); err != nil {
		return fmt.Errorf("mount the state disk: %w", err)
	}

	for _, d := range stateDirs {
		source := stateMount + "/" + d.dir

		if err := sys.Own(source); err != nil {
			return fmt.Errorf("make %s: %w", source, err)
		}

		if err := sys.Mount(source, d.target, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind %s on %s: %w", source, d.target, err)
		}
	}

	// it is on PATH and the prompt says to install there
	if err := sys.Own(localBin); err != nil {
		return fmt.Errorf("make %s: %w", localBin, err)
	}

	return nil
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

// serve connects to the terminal on the host and serves the session on it.
func serve(sys System, options Options, console io.Writer) error {
	if options.TerminalPort == 0 {
		return ErrNoTerminal
	}

	conn, err := sys.DialHost(options.TerminalPort)
	if err != nil {
		return fmt.Errorf("connect to the terminal on the host: %w", err)
	}

	defer func() { _ = conn.Close() }()

	return session.Serve(conn, func(request session.Request) (session.Process, error) {
		return start(sys, options, request, console)
	})
}

// start runs the command on a new terminal of the size the host asked for.
func start(sys System, options Options, request session.Request, console io.Writer) (*process, error) {
	pty, err := session.OpenPTY(request.Size)
	if err != nil {
		return nil, fmt.Errorf("open a terminal: %w", err)
	}

	// programs that open their terminal by name need to own it
	if err := pty.Slave.Chown(int(vm.GuestUID), int(vm.GuestGID)); err != nil {
		say(console, "aibox: own the terminal: %v\n", err)
	}

	if rejected := refused(options, request); len(rejected) > 0 {
		say(console, "aibox: %s stay as the VM sets them\n", strings.Join(rejected, ", "))
	}

	cmd := Command(options, pty.Slave, request)

	pid, err := sys.Start(cmd, sessionCgroup)

	_ = pty.Slave.Close()

	if err != nil {
		_ = pty.Master.Close()

		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}

	return &process{pty: pty, pid: pid, sys: sys}, nil
}

// process is the command on its terminal.
type process struct {
	pty *session.PTY
	pid int
	sys System
}

func (p *process) Read(b []byte) (int, error)  { return p.pty.Master.Read(b) }
func (p *process) Write(b []byte) (int, error) { return p.pty.Master.Write(b) }

func (p *process) Resize(size session.Size) error { return p.pty.Resize(size) }

func (p *process) Close() error { return p.pty.Master.Close() }

// Wait reaps every child until the command exits. As PID 1 the init also
// inherits the children whose parents are gone.
func (p *process) Wait() (int, error) {
	for {
		exited, exitCode, err := p.sys.Wait()
		if err != nil {
			return 0, err
		}

		if exited == p.pid {
			return exitCode, nil
		}
	}
}

func say(console io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(console, format, args...)
}
