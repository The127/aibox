package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/anthropic"
	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/gitbroker"
	"github.com/the127/aibox/internal/gitconfig"
	"github.com/the127/aibox/internal/image"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/project"
	"github.com/the127/aibox/internal/proxy"
)

// The programs that run the VM and the proxy would run with root rights on
// the host, and nothing here needs them.
var errRoot = errors.New("do not run aibox as root, start it as a normal user")

// Claude Code and the shell in the VM wait for keys, so without a terminal
// on stdin a run would sit there forever.
var errNoTerminal = errors.New("run needs a terminal on stdin")

// The project folder is shared into the VM read-write. From the home or
// above it the VM would get the configs and logins of every project, the
// shell files and keys of the person, and could change its own allow list.
var errNotAProject = errors.New("run must start in a project folder")

// The skills of the person are shared from their home on the host into the
// home of the VM.
const (
	hostSkillsDir  = ".claude/skills"
	guestSkillsDir = "/home/user/.claude/skills"
)

func runCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:  "run",
		Usage: "boot the VM with the current folder shared into it",
		Flags: append(vmFlags(),
			&cli.BoolFlag{Name: "shell", Usage: "open a shell in the VM instead of Claude Code"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return run(ctx, deps, cmd)
		},
	}
}

// vmFlags are the flags of every command that boots the VM.
func vmFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "image", Usage: "folder with the image of the VM", DefaultText: "the image a package installed, else the image of this release, or ~/.aibox/image for a build from a checkout"},
		&cli.IntFlag{Name: "memory", Usage: "memory of the VM in MiB", Value: 2048},
		&cli.IntFlag{Name: "cpus", Usage: "number of CPUs of the VM", Value: 2},
		&cli.BoolFlag{Name: "no-sandbox", Usage: "run the VM outside its sandbox, to debug it"},
	}
}

func run(ctx context.Context, deps dependencies, cmd *cli.Command) error {
	if deps.uid() == 0 {
		return errRoot
	}

	if !deps.stdinIsTerminal() {
		return errNoTerminal
	}

	cwd, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	home, err := deps.homeDir()
	if err != nil {
		return fmt.Errorf("find the home directory: %w", err)
	}

	if err := refuseUnsafeFolder(cwd, home, aibox); err != nil {
		return err
	}

	r, err := openProjectRun(ctx, deps, cmd, cwd, aibox)
	if err != nil {
		return err
	}

	defer r.close()

	// the VM has a home of its own, so git there knows nothing of the person
	if identity := deps.gitIdentity(cwd); identity != (gitconfig.Identity{}) {
		if err := gitconfig.Write(r.project.Home, identity); err != nil {
			return err
		}
	}

	spec := r.spec(cmd)
	spec.State = r.project.State
	spec.Project = cwd
	spec.Home = r.project.Home
	spec.Shell = cmd.Bool("shell")
	spec.ConsoleLog = r.project.ConsoleLog
	spec.Stdin = os.Stdin
	spec.Stdout = os.Stdout

	return deps.backend.Run(ctx, spec)
}

// folders returns the current folder and the aibox folder.
func folders(deps dependencies) (cwd, aibox string, err error) {
	if cwd, err = deps.getwd(); err != nil {
		return "", "", fmt.Errorf("find the current folder: %w", err)
	}

	if aibox, err = deps.aiboxDir(); err != nil {
		return "", "", fmt.Errorf("find the aibox folder: %w", err)
	}

	return cwd, aibox, nil
}

// projectRun is what booting the VM for the project in the current folder
// needs, for any command: the image, the folders of the project, its
// config, the mounts, the variables and the log of the proxy.
type projectRun struct {
	image string
	// kernelDigest is what the kernel of the image must hash to, or empty
	// for an image the person gave
	kernelDigest string
	project      project.Project
	config       config.Config
	mounts       []backend.Mount
	env          []string
	log          *os.File
	// proxyLog writes the hosts the proxy connected to or refused into log
	proxyLog *proxy.Log
	// forwarder sends the requests of Claude Code to the Claude API with
	// the API key, or is nil when the VM gets no key
	forwarder http.Handler
	// gitRemotes are the remotes the git broker serves with the logins of
	// the host, and roots the certificates it checks their servers with
	gitRemotes []gitbroker.Remote
	roots      *x509.CertPool
	// sshKeys hold the connection to the SSH agent the broker signs with,
	// or are nil
	sshKeys sshKeys
}

func openProjectRun(ctx context.Context, deps dependencies, cmd *cli.Command, cwd, aibox string) (projectRun, error) {
	for _, flag := range []string{"memory", "cpus"} {
		if cmd.Int(flag) < 1 {
			return projectRun{}, fmt.Errorf("--%s must be at least 1", flag)
		}
	}

	vmImage := cmd.String("image")
	kernelDigest := ""

	if vmImage == "" {
		var err error
		if vmImage, err = defaultImage(ctx, deps, filepath.Join(aibox, "image")); err != nil {
			return projectRun{}, err
		}

		kernelDigest, _ = deps.kernelDigest(runtime.GOARCH)
	}

	if _, _, err := machine.Image(vmImage); err != nil {
		return projectRun{}, err
	}

	p, err := project.Open(filepath.Join(aibox, "projects"), cwd)
	if err != nil {
		return projectRun{}, fmt.Errorf("open the project folder: %w", err)
	}

	cfg, err := config.Load(p.Config)
	if err != nil {
		return projectRun{}, err
	}

	mounts, err := mountFolders(deps.homeDir, cfg.Mounts, cfg.Skills)
	if err != nil {
		return projectRun{}, err
	}

	env, err := hostVariables(cfg, deps.lookupEnv)
	if err != nil {
		return projectRun{}, err
	}

	env, key, err := keepAPIKey(env, cfg.Allow.LoopbackPorts())
	if err != nil {
		return projectRun{}, err
	}

	env, err = routeGit(env, cfg.Git, cfg.Allow.LoopbackPorts())
	if err != nil {
		return projectRun{}, err
	}

	remotes, sshKeys, err := gitLogins(ctx, deps, cfg.Git)
	if err != nil {
		return projectRun{}, err
	}

	var (
		forwarder http.Handler
		roots     *x509.CertPool
	)

	if key != "" || len(remotes) > 0 {
		if roots, err = systemRoots(); err != nil {
			closeKeys(sshKeys)

			return projectRun{}, fmt.Errorf("read the root certificates: %w", err)
		}
	}

	if key != "" {
		forwarder = anthropic.Forwarder(key, roots)
	}

	log, err := os.OpenFile(p.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		closeKeys(sshKeys)

		return projectRun{}, fmt.Errorf("open the log of the proxy: %w", err)
	}

	return projectRun{
		image: vmImage, kernelDigest: kernelDigest, project: p, config: cfg, mounts: mounts, env: env, log: log, proxyLog: proxy.NewLog(log),
		forwarder: forwarder, gitRemotes: remotes, roots: roots, sshKeys: sshKeys,
	}, nil
}

func (r projectRun) close() {
	_ = r.log.Close()
	closeKeys(r.sshKeys)
}

func closeKeys(keys sshKeys) {
	if keys != nil {
		keys.Close()
	}
}

// spec is the part of the spec that every command fills the same way.
func (r projectRun) spec(cmd *cli.Command) backend.Spec {
	spec := backend.Spec{
		Image:        r.image,
		KernelDigest: r.kernelDigest,
		StateBytes:   stateBytes(r.config),
		MemoryMiB:    flagOrConfig(cmd, "memory", r.config.Memory),
		CPUs:         flagOrConfig(cmd, "cpus", r.config.CPUs),
		Mounts:       r.mounts,
		Unsandboxed:  cmd.Bool("no-sandbox"),
		Env:          r.env,
		Proxy: proxy.Options{
			Allow:       r.config.Allow.Allows,
			Pinned:      r.config.Allow.Pinned,
			OnConnected: r.proxyLog.Connected,
			OnRefused:   r.proxyLog.Refused,
			Hint:        "Add it to " + r.project.Config + " to allow it.",
		},
		Ports:    r.config.Allow.Ports(),
		Loopback: r.config.Allow.LoopbackPorts(),
		Stderr:   os.Stderr,
	}

	spec.Proxy.Local = r.local(r.log)

	if r.forwarder != nil {
		spec.Loopback = append(spec.Loopback, forwarderPort)
	}

	if len(r.gitRemotes) > 0 {
		spec.Loopback = append(spec.Loopback, brokerPort)
	}

	// aibox itself connects to the Claude API and to the git servers
	if (r.forwarder != nil || len(r.gitRemotes) > 0) && !slices.Contains(spec.Ports, apiPort) {
		spec.Ports = append(spec.Ports, apiPort)
	}

	for _, remote := range r.gitRemotes {
		if remote.SSH == nil {
			continue
		}

		_, port, _ := net.SplitHostPort(remote.SSH.Address)
		if n, err := strconv.ParseUint(port, 10, 16); err == nil && !slices.Contains(spec.Ports, uint16(n)) {
			spec.Ports = append(spec.Ports, uint16(n))
		}
	}

	return spec
}

// local returns the handlers the proxy serves itself on the loopback of
// the VM: the forwarder of the API key and the git broker, which writes
// to log. It is nil when the VM gets neither.
func (r projectRun) local(log io.Writer) func(host, port string) http.Handler {
	if r.forwarder == nil && len(r.gitRemotes) == 0 {
		return nil
	}

	handlers := map[string]http.Handler{}

	if r.forwarder != nil {
		handlers[strconv.Itoa(forwarderPort)] = r.forwarder
	}

	if len(r.gitRemotes) > 0 {
		handlers[strconv.Itoa(brokerPort)] = gitbroker.Broker(r.gitRemotes, r.roots, log)
	}

	return func(host, port string) http.Handler {
		if host != "localhost" {
			return nil
		}

		return handlers[port]
	}
}

// forwarderPort is the port on the loopback of the VM where Claude Code
// reaches the forwarder, brokerPort the one where git reaches the git
// broker, and apiPort the one of the Claude API and the git servers.
const (
	forwarderPort = 3129
	brokerPort    = 3130
	apiPort       = 443
)

// errBrokerPort is a port of the allow list that the git broker takes on
// the loopback of the VM.
var errBrokerPort = fmt.Errorf("allow names port %d of the loopback, which the VM needs for git", brokerPort)

// errOwnGitConfig is a git config set by env, where aibox puts the
// addresses of the git broker.
var errOwnGitConfig = errors.New("env sets GIT_CONFIG_COUNT, GIT_CONFIG_KEY_* or GIT_CONFIG_VALUE_*, which aibox sets itself for git")

// routeGit adds to the variables for the VM a git config that sends the
// remotes of the config to the git broker, whether git names them by
// HTTPS or by SSH. The broker refuses what the config does not allow.
func routeGit(env []string, remotes []config.GitRemote, loopback []uint16) ([]string, error) {
	if len(remotes) == 0 {
		return env, nil
	}

	if slices.Contains(loopback, brokerPort) {
		return nil, errBrokerPort
	}

	if slices.ContainsFunc(env, func(variable string) bool {
		return strings.HasPrefix(variable, "GIT_CONFIG_COUNT=") || strings.HasPrefix(variable, "GIT_CONFIG_KEY_") || strings.HasPrefix(variable, "GIT_CONFIG_VALUE_")
	}) {
		return nil, errOwnGitConfig
	}

	var count int

	for _, remote := range remotes {
		host, repository, _ := strings.Cut(remote.Remote, "/")
		key := fmt.Sprintf("url.http://127.0.0.1:%d/%s.insteadOf", brokerPort, remote.Remote)

		for _, address := range []string{"https://" + remote.Remote, "git@" + host + ":" + repository, "ssh://git@" + host + "/" + repository} {
			env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", count, key), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", count, address))
			count++
		}
	}

	return append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", count)), nil
}

// gitLogins asks git on the host for its login for each remote, before the
// VM starts, since aibox can start no programs once it is confined. When
// git has no login for HTTPS, the remote goes over SSH with the SSH keys
// of the host, which it returns to be closed with the run.
func gitLogins(ctx context.Context, deps dependencies, remotes []config.GitRemote) ([]gitbroker.Remote, sshKeys, error) {
	var (
		brokered []gitbroker.Remote
		keys     sshKeys
		keysErr  error
		loaded   bool
	)

	for _, remote := range remotes {
		brokeredRemote := gitbroker.Remote{Name: remote.Remote, Fetch: remote.Fetch, Push: remote.Push}

		login, err := deps.gitLogin(ctx, remote.Remote)

		switch {
		case err == nil:
			brokeredRemote.Login = gitbroker.Login{Username: login.Username, Password: login.Password}
		case errors.Is(err, gitconfig.ErrNoLogin):
			if !loaded {
				if keys, keysErr = deps.sshKeys(); keysErr != nil {
					keys = nil
				}

				loaded = true
			}

			host, _, _ := strings.Cut(remote.Remote, "/")

			var target gitconfig.SSHTarget
			if keysErr == nil {
				target, err = keys.For(ctx, host)
			} else {
				err = keysErr
			}

			if err != nil {
				closeKeys(keys)

				return nil, nil, fmt.Errorf("git entry %s: git on this machine has no login for HTTPS, and SSH does not work either: %w", remote.Remote, err)
			}

			_, _ = fmt.Fprintf(deps.stderr, "aibox: git has no login for HTTPS for %s, so the broker reaches it over SSH as %s@%s with %s\n",
				remote.Remote, target.User, target.Address, strings.Join(target.Keys, ", "))

			brokeredRemote.SSH = &gitbroker.SSH{
				Address: target.Address, User: target.User, Signers: target.Signers,
				HostKey: target.HostKey, HostKeyAlgorithms: target.HostKeyAlgorithms,
			}
		default:
			closeKeys(keys)

			return nil, nil, fmt.Errorf("git entry %s: %w", remote.Remote, err)
		}

		brokered = append(brokered, brokeredRemote)
	}

	return brokered, keys, nil
}

// errOwnBaseURL is ANTHROPIC_BASE_URL set by the config next to the API key,
// which would send the requests elsewhere than to the forwarder.
var errOwnBaseURL = errors.New("env sets ANTHROPIC_BASE_URL next to ANTHROPIC_API_KEY, but aibox keeps the key on the host and sets ANTHROPIC_BASE_URL itself")

// errForwarderPort is a port of the allow list that the forwarder takes on
// the loopback of the VM.
var errForwarderPort = fmt.Errorf("allow names port %d of the loopback, which the VM needs for the API key", forwarderPort)

// keepAPIKey takes the API key out of the variables for the VM and puts in
// a placeholder and the address of the forwarder, so that the VM never
// holds the key. It returns the key, or nothing when the variables have
// none. A token of a claude.ai subscription goes into the VM as it is,
// since only its owner may use it.
func keepAPIKey(env []string, loopback []uint16) ([]string, string, error) {
	var key string

	for i, variable := range env {
		if value, ok := strings.CutPrefix(variable, "ANTHROPIC_API_KEY="); ok && value != "" {
			key = value
			env[i] = "ANTHROPIC_API_KEY=" + anthropic.Placeholder
		}
	}

	if key == "" {
		return env, "", nil
	}

	if slices.ContainsFunc(env, func(variable string) bool { return strings.HasPrefix(variable, "ANTHROPIC_BASE_URL=") }) {
		return nil, "", errOwnBaseURL
	}

	if slices.Contains(loopback, forwarderPort) {
		return nil, "", errForwarderPort
	}

	return append(env, fmt.Sprintf("ANTHROPIC_BASE_URL=http://127.0.0.1:%d", forwarderPort)), key, nil
}

// maxPathBytes is what the PATH for the VM may be long. The session carries
// it in one packet.
const maxPathBytes = 16 * 1024

// hostVariables returns the variables for the VM as NAME=value: the env
// entries, with the values of the pass-through ones from the host, and the
// PATH from the path entries.
func hostVariables(cfg config.Config, lookup func(string) (string, bool)) ([]string, error) {
	env, err := environment(cfg.Env, lookup)
	if err != nil {
		return nil, err
	}

	folders, err := pathFolders(cfg.Path, lookup)
	if err != nil {
		return nil, err
	}

	if len(folders) == 0 {
		return env, nil
	}

	path := strings.Join(folders, ":")
	if len(path) > maxPathBytes {
		return nil, fmt.Errorf("path: %d bytes of folders are too many for the VM", len(path))
	}

	return append(env, "PATH="+path), nil
}

// pathFolders returns the folders of the path entries once each. An entry
// that names a variable of the host stands for the folders in it, less the
// ones that cannot be on the PATH of the VM.
func pathFolders(entries []string, lookup func(string) (string, bool)) ([]string, error) {
	var folders []string

	for _, entry := range entries {
		if filepath.IsAbs(entry) {
			folders = appendFolder(folders, entry)

			continue
		}

		value, ok := lookup(entry)
		if !ok {
			return nil, fmt.Errorf("path %s is not set on the host", entry)
		}

		for _, folder := range strings.Split(value, ":") {
			if filepath.IsAbs(folder) && filepath.Clean(folder) != "/" {
				folders = appendFolder(folders, filepath.Clean(folder))
			}
		}
	}

	return folders, nil
}

func appendFolder(folders []string, folder string) []string {
	if slices.Contains(folders, folder) {
		return folders
	}

	return append(folders, folder)
}

// environment returns the variables of the config as NAME=value, taking
// the values of the pass-through ones from the host.
func environment(variables []config.Variable, lookup func(string) (string, bool)) ([]string, error) {
	env := make([]string, 0, len(variables))

	for _, variable := range variables {
		value := variable.Value

		if variable.FromHost {
			var ok bool

			value, ok = lookup(variable.Name)
			if !ok {
				return nil, fmt.Errorf("env %s is not set on the host", variable.Name)
			}
		}

		env = append(env, variable.Name+"="+value)
	}

	return env, nil
}

// refuseUnsafeFolder is errNotAProject when the folder is the root, the
// home directory or above it, or above the aibox folder.
func refuseUnsafeFolder(cwd, home, aibox string) error {
	if holds(cwd, "/") {
		return fmt.Errorf("%w, not the root of the file system", errNotAProject)
	}

	if holds(cwd, home) {
		return fmt.Errorf("%w, not one that holds the home directory %s", errNotAProject, home)
	}

	if holds(cwd, aibox) {
		return fmt.Errorf("%w, not one that holds %s", errNotAProject, aibox)
	}

	return nil
}

// holds tells whether the folder is the one at path or holds it. It
// compares files, not names, since a folder has more names than one: a
// symlink, another case where the file system tells no case apart, a
// firmlink of macOS or a bind mount. The folder holds the path when the
// path, with some of its first folders cut off, leads from the folder to
// the same file. So /System/Volumes/Data of a Mac holds /Users/you, though
// .. of /Users is /. A path that does not exist yet stands for the first
// folder above it that does. A symlink in the folder that leads to the
// path makes it refused too, which errs on the safe side.
func holds(folder, path string) bool {
	path = resolved(path)

	target, err := os.Stat(path)
	for err != nil && path != filepath.Dir(path) {
		path = filepath.Dir(path)
		target, err = os.Stat(path)
	}

	if err != nil {
		return false
	}

	names := strings.Split(strings.Trim(path, "/"), "/")
	for i := range len(names) + 1 {
		info, err := os.Stat(filepath.Join(append([]string{folder}, names[i:]...)...))
		if err == nil && os.SameFile(info, target) {
			return true
		}
	}

	return false
}

// resolved is the path with its symlinks followed, or the path as it is
// when that fails, for example because it does not exist yet.
func resolved(path string) string {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		return target
	}

	return filepath.Clean(path)
}

// defaultImage is the folder of the image that this aibox uses. An image a
// package installed comes first. Else a release uses the image it was
// released with, which it downloads the first time and which replaces the
// images of the releases before. A build from a checkout uses the one just
// install-image puts right into parent.
func defaultImage(ctx context.Context, deps dependencies, parent string) (string, error) {
	if dir := deps.systemImage; isFolder(dir) {
		if _, _, err := machine.Image(dir); err == nil {
			return dir, nil
		}

		_, _ = fmt.Fprintf(deps.stderr, "aibox: %s holds no complete VM image, so aibox does not use it\n", dir)
	}

	version := deps.version()
	digest, ok := deps.imageDigest(runtime.GOARCH)

	if !image.Released(version) || !ok {
		return parent, nil
	}

	dir := filepath.Join(parent, version)
	if _, _, err := machine.Image(dir); err == nil {
		return dir, nil
	}

	_, _ = fmt.Fprintf(os.Stderr, "aibox: downloading the VM image of %s\n", version)

	if err := deps.fetchImage(ctx, version, runtime.GOARCH, digest, dir); err != nil {
		return "", fmt.Errorf("download the VM image of %s: %w. --image runs with an image of your own instead", version, err)
	}

	if err := image.Prune(parent, version); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "aibox: remove the images of older releases: %v\n", err)
	}

	return dir, nil
}

// defaultDiskGiB is the size of the state disk unless the config says
// otherwise. It is sparse, so the number costs nothing until used.
const defaultDiskGiB = 16

func stateBytes(cfg config.Config) int64 {
	gib := defaultDiskGiB
	if cfg.Disk != nil {
		gib = *cfg.Disk
	}

	return int64(gib) << 30
}

// mountFolders returns the mount of the skills of the person, when the host
// has any, the config does not turn them off and no mount of the config
// takes their place, and a mount for each mount of the config, once the
// host folders are known to exist.
func mountFolders(homeDir func() (string, error), mounts []config.Mount, skillsSetting config.Skills) ([]backend.Mount, error) {
	home, err := homeDir()
	if err != nil {
		return nil, fmt.Errorf("find the home folder: %w", err)
	}

	folders := make([]backend.Mount, 0, len(mounts)+1)

	// a person without skills is the normal case, so a missing folder is
	// nothing to report
	skills := filepath.Join(home, hostSkillsDir)
	if skillsSetting != config.SkillsNone && isFolder(skills) && !slices.ContainsFunc(mounts, func(m config.Mount) bool { return m.Touches(guestSkillsDir) }) {
		folders = append(folders, backend.Mount{Host: skills, Guest: guestSkillsDir})
	}

	for _, mount := range mounts {
		info, err := os.Stat(mount.Host)
		if err != nil {
			return nil, fmt.Errorf("mount %s: %w", mount.Guest, err)
		}

		if !info.IsDir() {
			return nil, fmt.Errorf("mount %s: %s is not a folder", mount.Guest, mount.Host)
		}

		folders = append(folders, backend.Mount{Host: mount.Host, Guest: mount.Guest})
	}

	return folders, nil
}

func isFolder(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

// flagOrConfig returns the flag if it was given on the command line, else
// the config value if the file has one, else the default of the flag.
func flagOrConfig(cmd *cli.Command, flag string, configured *int) int {
	// a flag on the command line is the most deliberate of the three
	if !cmd.IsSet(flag) && configured != nil {
		return *configured
	}

	return cmd.Int(flag)
}
