package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/anthropic"
	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/clipboard"
	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/gitbroker"
	"github.com/the127/aibox/internal/gitconfig"
	"github.com/the127/aibox/internal/image"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/project"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/realpath"
	"github.com/the127/aibox/internal/vm"
)

// The programs that run the VM and the proxy would run with root rights on
// the host, and nothing here needs them.
var errRoot = errors.New("do not run aibox as root, start it as a normal user")

// Claude Code and the shell in the VM wait for keys, so without a terminal
// on stdin a run would sit there forever.
var errNoTerminal = errors.New("run needs a terminal on stdin")

var errResumeShell = errors.New("--resume goes on with a session of Claude Code, so it does not go with --shell")

// The project folder is shared into the VM read-write. From the home or
// above it the VM would get the configs and logins of every project, the
// shell files and keys of the person, and could change its own allow list.
var errNotAProject = errors.New("run must start in a project folder")

// A mount is read-only, but from the home or ~/.aibox the VM would still read
// the configs and logins of every project and the keys of the person.
var errUnsafeMount = errors.New("the VM must not see this folder")

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
			&cli.StringFlag{Name: "resume", Usage: "go on with the Claude Code session with this ID"},
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

	resume := cmd.String("resume")
	if resume != "" {
		if cmd.Bool("shell") {
			return errResumeShell
		}

		if err := vm.CheckSessionID(resume); err != nil {
			return fmt.Errorf("--resume %w", err)
		}
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

	r, err := openProjectRun(ctx, deps, cmd, cwd, aibox, lockRunOf)
	if err != nil {
		return err
	}

	defer r.close()

	r.env = append(r.env, identityVariables(deps.gitIdentity(cwd), r.env)...)

	// git in the VM reads the files of the project as git on the host wrote
	// them, with the line endings the config of the host chose; without it,
	// every file differs from the repository in the VM. A task clones the
	// repository in the VM, which writes them as the VM's git does.
	if value := deps.gitAutoCRLF(cwd); value != "" {
		r.env = addAutoCRLF(r.env, value)
	}

	shareClaudeSetup(home, r.project.Home, r.config, deps.stderr)

	if r.config.IDE {
		if err := checkIDE(r.config.Allow.LoopbackPorts(), r.env); err != nil {
			return err
		}

		found, ok, err := findIDE(deps, cwd)
		if err != nil {
			return err
		}

		if ok {
			env, bridge, err := linkIDE(found, cwd, r.project.Home, r.log)
			if err != nil {
				return err
			}

			r.env, r.ide, r.ideHostPort = append(r.env, env...), bridge, found.Port
		}
	}

	if r.config.Clipboard == nil || *r.config.Clipboard {
		gate, helper, err := shareClipboard(deps, r.config.Allow.LoopbackPorts(), r.log)
		if err != nil {
			return err
		}

		defer func() { _ = helper.Close() }()

		r.clipboard = gate
	}

	spec := r.spec(cmd)
	spec.State = r.project.State
	spec.Project = cwd
	spec.Home = r.project.Home
	spec.Shell = cmd.Bool("shell")
	spec.Resume = resume
	spec.ConsoleLog = r.project.ConsoleLog
	spec.Stdin = os.Stdin
	spec.Stdout = os.Stdout

	if r.config.Mise {
		if spec.Mise, err = approveMise(ctx, deps, cwd, r.project); err != nil {
			return err
		}
	}

	// aibox can't listen on a port once it has locked itself down
	spec.Forwards, err = listenPorts(r.config.Ports, r.config.Allow.LoopbackPorts())
	if err != nil {
		return err
	}

	defer closeForwards(spec.Forwards)

	watch := append(slices.Clone(watched), r.config.Watch...)
	files := takeSnapshot(cwd, watch)
	before := sessions(r.project.Home)
	err = deps.backend.Run(ctx, spec)
	r.proxyLog.Summarize()

	if id, ok := lastSession(before, sessions(r.project.Home)); ok {
		_, _ = fmt.Fprintf(deps.stderr, "aibox: to go on with this session, run aibox run --resume %s\n", id)
	}

	reportChanges(deps.stderr, files, takeSnapshot(cwd, watch))

	return err
}

// identityVariables returns the variables that give git in the VM the
// identity of the person, since the VM has a home of its own. They are
// variables and not a file in that home, because the VM can put links into
// the home that a write by the host would follow. A variable of the env
// setting wins, since the person set it on purpose.
func identityVariables(identity gitconfig.Identity, env []string) []string {
	var variables []string

	for _, v := range []struct{ name, value string }{
		{"GIT_AUTHOR_NAME", identity.Name},
		{"GIT_COMMITTER_NAME", identity.Name},
		{"GIT_AUTHOR_EMAIL", identity.Email},
		{"GIT_COMMITTER_EMAIL", identity.Email},
	} {
		set := slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, v.name+"=") })

		if v.value != "" && !set {
			variables = append(variables, v.name+"="+v.value)
		}
	}

	return variables
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
	// lock is the lock of the project the command holds while it runs
	lock   *os.File
	config config.Config
	mounts []backend.Mount
	env    []string
	log    *os.File
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
	// ide serves the link to the IDE of the host, or is nil when the VM
	// gets none, and ideHostPort is the port the IDE listens on
	ide         http.Handler
	ideHostPort uint16
	// clipboard lets the VM read the clipboard right after Ctrl+V, or is
	// nil when the VM gets none
	clipboard *clipboard.Gate
}

// openProjectRun takes the lock of the project with lock, before it makes
// the home, so that a reset or a remove never works in a home a VM uses.
func openProjectRun(ctx context.Context, deps dependencies, cmd *cli.Command, cwd, aibox string, lock func(dir string) (*os.File, error)) (projectRun, error) {
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

	at, err := project.At(filepath.Join(aibox, "projects"), cwd)
	if err != nil {
		return projectRun{}, fmt.Errorf("open the project folder: %w", err)
	}

	held, err := lock(at.Dir)
	if err != nil {
		return projectRun{}, fmt.Errorf("open the project folder: %w", err)
	}

	r, err := openLockedProjectRun(ctx, deps, cwd, aibox, vmImage, kernelDigest)
	if err != nil {
		_ = held.Close()

		return projectRun{}, err
	}

	r.lock = held

	return r, nil
}

func openLockedProjectRun(ctx context.Context, deps dependencies, cwd, aibox, vmImage, kernelDigest string) (projectRun, error) {
	p, err := project.Open(filepath.Join(aibox, "projects"), cwd)
	if err != nil {
		return projectRun{}, fmt.Errorf("open the project folder: %w", err)
	}

	cfg, err := loadConfig(deps, p.Config, aibox)
	if err != nil {
		return projectRun{}, err
	}

	mounts, err := mountFolders(deps.homeDir, cfg)
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

	if r.lock != nil {
		_ = r.lock.Close()
	}
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
			OnClosed:    r.proxyLog.Closed,
			Hint:        config.AllowHint(r.project.Config),
		},
		Ports:     r.config.Allow.Ports(),
		AllowList: r.config.Allow,
		AllowFile: r.project.Config,
		Loopback:  r.config.Allow.LoopbackPorts(),
		Stderr:    os.Stderr,
	}

	spec.Proxy.Local = r.local(r.log)

	if r.forwarder != nil {
		spec.Loopback = append(spec.Loopback, forwarderPort)
	}

	if len(r.gitRemotes) > 0 {
		spec.Loopback = append(spec.Loopback, brokerPort)
	}

	if r.clipboard != nil {
		spec.Loopback = append(spec.Loopback, vm.ClipboardPort)
		spec.OnPaste = r.clipboard.Paste
		// the terminals of Windows paste on Ctrl+V themselves
		spec.PasteAltV = runtime.GOOS == "windows"
		if r.config.PasteAltV != nil {
			spec.PasteAltV = *r.config.PasteAltV
		}
	}

	if r.ide != nil {
		spec.Loopback = append(spec.Loopback, idePort)

		// aibox itself connects to the IDE
		if !slices.Contains(spec.Ports, r.ideHostPort) {
			spec.Ports = append(spec.Ports, r.ideHostPort)
		}
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
// the VM: the forwarder of the API key, the git broker, which writes to
// log, the link to the IDE and the clipboard. It is nil when the VM gets
// none of them.
func (r projectRun) local(log io.Writer) func(host, port string) http.Handler {
	if r.forwarder == nil && len(r.gitRemotes) == 0 && r.ide == nil && r.clipboard == nil {
		return nil
	}

	handlers := map[string]http.Handler{}

	if r.forwarder != nil {
		handlers[strconv.Itoa(forwarderPort)] = r.forwarder
	}

	if len(r.gitRemotes) > 0 {
		handlers[strconv.Itoa(brokerPort)] = gitbroker.Broker(r.gitRemotes, r.roots, log)
	}

	if r.ide != nil {
		handlers[strconv.Itoa(idePort)] = r.ide
	}

	if r.clipboard != nil {
		handlers[strconv.Itoa(int(vm.ClipboardPort))] = r.clipboard
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

// autoCRLF is the setting of git for the line endings of the files.
const autoCRLF = "core.autocrlf"

// addAutoCRLF adds core.autocrlf to the git config the variables give git
// in the VM, after the settings already there, such as those of routeGit.
// One the variables set already wins, since the person set it on purpose,
// and so does a count git would refuse, which the person fixes.
func addAutoCRLF(env []string, value string) []string {
	count := 0
	set := map[int]bool{}

	for _, variable := range env {
		if n, ok := strings.CutPrefix(variable, "GIT_CONFIG_COUNT="); ok {
			var err error
			if count, err = strconv.Atoi(n); err != nil || count < 0 {
				return env
			}
		}

		if entry, ok := strings.CutPrefix(variable, "GIT_CONFIG_KEY_"); ok {
			n, key, _ := strings.Cut(entry, "=")
			// section and variable names are the same in any case
			if i, err := strconv.Atoi(n); err == nil && strings.EqualFold(key, autoCRLF) {
				set[i] = true
			}
		}
	}

	for i := range set {
		if i < count {
			return env
		}
	}

	env = slices.DeleteFunc(slices.Clone(env), func(variable string) bool { return strings.HasPrefix(variable, "GIT_CONFIG_COUNT=") })

	return append(env,
		fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", count, autoCRLF),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", count, value),
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", count+1))
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

				return nil, nil, fmt.Errorf("git entry %s: git on this machine has no login for HTTPS, and SSH does not work either: %w (aibox asks for no login: store one with git, such as by a git push over HTTPS, or make SSH work)", remote.Remote, err)
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
		// folders of the VM, Linux paths, whatever the host
		if path.IsAbs(entry) {
			folders = appendFolder(folders, entry)

			continue
		}

		value, ok := lookup(entry)
		if !ok {
			return nil, fmt.Errorf("path %s is not set on the host", entry)
		}

		for _, folder := range strings.Split(value, ":") {
			if path.IsAbs(folder) && path.Clean(folder) != "/" {
				folders = appendFolder(folders, path.Clean(folder))
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
	if isRoot(cwd) {
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

// templateFile is the config new projects start from, in the aibox folder.
func templateFile(aibox string) string {
	return filepath.Join(aibox, "config.yaml")
}

// loadConfig returns the config in the file at path, or an error when one of
// its mounts is unsafe. A new project's config starts from the template,
// which has to pass the same checks before it is copied.
func loadConfig(deps dependencies, path, aibox string) (config.Config, error) {
	template := templateFile(aibox)

	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Lstat(template); err == nil {
			if _, err := checkedConfig(deps, template, "", aibox); err != nil {
				return config.Config{}, fmt.Errorf("the template: %w", err)
			}
		}
	}

	return checkedConfig(deps, path, template, aibox)
}

// checkedConfig loads the config at path, starting it from template, and
// refuses its unsafe mounts.
func checkedConfig(deps dependencies, path, template, aibox string) (config.Config, error) {
	cfg, err := config.Load(path, template)
	if err != nil {
		return config.Config{}, err
	}

	home, err := deps.homeDir()
	if err != nil {
		return config.Config{}, fmt.Errorf("find the home directory: %w", err)
	}

	if err := refuseUnsafeMounts(cfg.Mounts, home, aibox); err != nil {
		return config.Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	return cfg, nil
}

// refuseUnsafeMounts is errUnsafeMount when the host folder of a mount is the
// root, the home directory or above it, or the aibox folder, above it or
// inside it.
func refuseUnsafeMounts(mounts []config.Mount, home, aibox string) error {
	for _, mount := range mounts {
		switch {
		case isRoot(mount.Host):
			return fmt.Errorf("mount %s: %w, %s is the root of the file system", mount.Guest, errUnsafeMount, mount.Host)
		case holds(mount.Host, home):
			return fmt.Errorf("mount %s: %w, %s holds the home directory %s", mount.Guest, errUnsafeMount, mount.Host, home)
		case holds(mount.Host, aibox):
			return fmt.Errorf("mount %s: %w, %s holds %s", mount.Guest, errUnsafeMount, mount.Host, aibox)
		case holds(aibox, mount.Host):
			return fmt.Errorf("mount %s: %w, %s is inside %s", mount.Guest, errUnsafeMount, mount.Host, aibox)
		}
	}

	return nil
}

// holds tells whether the folder is the one at name or holds it. It
// compares files, not names, since a folder has more names than one: a
// symlink, another case where the file system tells no case apart, a
// firmlink of macOS or a bind mount. The folder holds the path when the
// path, with some of its first folders cut off, leads from the folder to
// the same file. So /System/Volumes/Data of a Mac holds /Users/you, though
// .. of /Users is /. A path that does not exist yet stands for the first
// folder above it that does. A symlink in the folder that leads to the
// path makes it refused too, which errs on the safe side. The folder must
// be absolute: on Windows C: alone is the folder the drive is at, and
// folders joined to it would be relative to that.
func holds(folder, name string) bool {
	name = realpath.Resolved(name)

	target, err := os.Stat(name)
	for err != nil && name != filepath.Dir(name) {
		name = filepath.Dir(name)
		target, err = os.Stat(name)
	}

	if err != nil {
		return false
	}

	// the folders of the path after its volume, C: or \\server\share on
	// Windows, where \ and / both part them
	names := strings.FieldsFunc(name[len(filepath.VolumeName(name)):], func(r rune) bool {
		return r == '/' || r == filepath.Separator
	})

	for i := range len(names) + 1 {
		info, err := os.Stat(filepath.Join(append([]string{folder}, names[i:]...)...))
		if err == nil && os.SameFile(info, target) {
			return true
		}
	}

	return false
}

// isRoot tells whether the folder is the root of a file system: / or, on
// Windows, the root of any drive or share, as / names only the drive
// aibox runs on there.
func isRoot(folder string) bool {
	resolved := realpath.Resolved(folder)

	return filepath.Dir(resolved) == resolved || holds(folder, "/")
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

	_, _ = fmt.Fprintf(deps.stderr, "aibox: downloading the VM image of %s\n", version)

	if err := deps.fetchImage(ctx, version, runtime.GOARCH, digest, dir); err != nil {
		return "", fmt.Errorf("download the VM image of %s: %w. --image runs with an image of your own instead", version, err)
	}

	if err := image.Prune(parent, version); err != nil {
		_, _ = fmt.Fprintf(deps.stderr, "aibox: remove the images of older releases: %v\n", err)
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

// mountFolders returns a mount for the skills, the agents and the commands
// of the person, each when the host has them, the config does not turn
// them off and no mount of the config takes their place, and a mount for
// each mount of the config, once the host folders are known to exist.
func mountFolders(homeDir func() (string, error), cfg config.Config) ([]backend.Mount, error) {
	home, err := homeDir()
	if err != nil {
		return nil, fmt.Errorf("find the home folder: %w", err)
	}

	mounts := cfg.Mounts
	folders := make([]backend.Mount, 0, len(mounts)+3)

	// a person without skills, agents or commands is the normal case, so a
	// missing folder is nothing to report
	for _, shared := range []struct {
		host, guest string
		setting     config.Share
	}{
		{hostSkillsDir, guestSkillsDir, cfg.Skills},
		{".claude/agents", "/home/user/.claude/agents", cfg.Agents},
		{".claude/commands", "/home/user/.claude/commands", cfg.Commands},
	} {
		host := filepath.Join(home, shared.host)
		if shared.setting != config.ShareNone && isFolder(host) && !slices.ContainsFunc(mounts, func(m config.Mount) bool { return m.Touches(shared.guest) }) {
			folders = append(folders, backend.Mount{Host: host, Guest: shared.guest})
		}
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

// lastSession returns the ID of the session that changed last from before
// to after, if one changed. Comparing the times of the files with each
// other needs no clock of the host or the VM.
func lastSession(before, after map[string]time.Time) (string, bool) {
	var last string

	var latest time.Time

	for id, changed := range after {
		if old, ok := before[id]; ok && old.Equal(changed) {
			continue
		}

		if last == "" || changed.After(latest) {
			last, latest = id, changed
		}
	}

	return last, last != ""
}

// sessions are the Claude Code sessions in the VM home, with the time each
// file changed last. The VM writes these folders, so only names that are
// session IDs count.
func sessions(vmHome string) map[string]time.Time {
	found := map[string]time.Time{}

	root, err := os.OpenRoot(vmHome)
	if err != nil {
		return found
	}

	defer func() { _ = root.Close() }()

	projects := filepath.Join(".claude", "projects")

	folders, err := readRootDir(root, projects)
	if err != nil {
		return found
	}

	for _, folder := range folders {
		if !folder.IsDir() {
			continue
		}

		files, err := readRootDir(root, filepath.Join(projects, folder.Name()))
		if err != nil {
			continue
		}

		for _, file := range files {
			id, ok := strings.CutSuffix(file.Name(), ".jsonl")
			if !ok || !file.Type().IsRegular() || vm.CheckSessionID(id) != nil {
				continue
			}

			if info, err := file.Info(); err == nil {
				found[id] = info.ModTime()
			}
		}
	}

	return found
}

// readRootDir reads a folder of the root without waiting, since the VM
// could have left a FIFO there.
func readRootDir(root *os.Root, name string) ([]fs.DirEntry, error) {
	dir, err := openInRoot(root, name)
	if err != nil {
		return nil, err
	}

	defer func() { _ = dir.Close() }()

	return dir.ReadDir(-1)
}
