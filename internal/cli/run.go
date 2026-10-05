package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/gitconfig"
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
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "image", Usage: "folder with the image of the VM", DefaultText: "~/.aibox/image"},
			&cli.IntFlag{Name: "memory", Usage: "memory of the VM in MiB", Value: 2048},
			&cli.IntFlag{Name: "cpus", Usage: "number of CPUs of the VM", Value: 2},
			&cli.BoolFlag{Name: "shell", Usage: "open a shell in the VM instead of Claude Code"},
			&cli.BoolFlag{Name: "no-sandbox", Usage: "run the VM outside its sandbox, to debug it"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return run(ctx, deps, cmd)
		},
	}
}

func run(ctx context.Context, deps dependencies, cmd *cli.Command) error {
	if deps.uid() == 0 {
		return errRoot
	}

	if !deps.stdinIsTerminal() {
		return errNoTerminal
	}

	for _, flag := range []string{"memory", "cpus"} {
		if cmd.Int(flag) < 1 {
			return fmt.Errorf("--%s must be at least 1", flag)
		}
	}

	cwd, err := deps.getwd()
	if err != nil {
		return fmt.Errorf("find the current folder: %w", err)
	}

	aibox, err := deps.aiboxDir()
	if err != nil {
		return fmt.Errorf("find the aibox folder: %w", err)
	}

	home, err := deps.homeDir()
	if err != nil {
		return fmt.Errorf("find the home directory: %w", err)
	}

	if err := refuseUnsafeFolder(cwd, home, aibox); err != nil {
		return err
	}

	image := cmd.String("image")
	if image == "" {
		image = filepath.Join(aibox, "image")
	}

	if _, _, err := machine.Image(image); err != nil {
		return err
	}

	p, err := project.Open(filepath.Join(aibox, "projects"), cwd)
	if err != nil {
		return fmt.Errorf("open the project folder: %w", err)
	}

	cfg, err := config.Load(p.Config)
	if err != nil {
		return err
	}

	mounts, err := mountFolders(deps.homeDir, cfg.Mounts)
	if err != nil {
		return err
	}

	env, err := hostVariables(cfg, deps.lookupEnv)
	if err != nil {
		return err
	}

	// the VM has a home of its own, so git there knows nothing of the person
	if identity := deps.gitIdentity(cwd); identity != (gitconfig.Identity{}) {
		if err := gitconfig.Write(p.Home, identity); err != nil {
			return err
		}
	}

	log, err := os.OpenFile(p.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open the log of refused hosts: %w", err)
	}

	defer func() { _ = log.Close() }()

	return deps.backend.Run(ctx, backend.Spec{
		Image:       image,
		State:       p.State,
		StateBytes:  stateBytes(cfg),
		MemoryMiB:   flagOrConfig(cmd, "memory", cfg.Memory),
		CPUs:        flagOrConfig(cmd, "cpus", cfg.CPUs),
		Project:     cwd,
		Home:        p.Home,
		Mounts:      mounts,
		Shell:       cmd.Bool("shell"),
		Unsandboxed: cmd.Bool("no-sandbox"),
		Env:         env,
		Proxy: proxy.Options{
			Allow:     cfg.Allow.Allows,
			OnRefused: proxy.RefusalLog(log),
			Hint:      "Add it to " + p.Config + " to allow it.",
		},
		Ports:      cfg.Allow.Ports(),
		ConsoleLog: p.ConsoleLog,
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	})
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
// has any and no mount of the config takes their place, and a mount for
// each mount of the config, once the host folders are known to exist.
func mountFolders(homeDir func() (string, error), mounts []config.Mount) ([]backend.Mount, error) {
	home, err := homeDir()
	if err != nil {
		return nil, fmt.Errorf("find the home folder: %w", err)
	}

	folders := make([]backend.Mount, 0, len(mounts)+1)

	// a person without skills is the normal case, so a missing folder is
	// nothing to report
	skills := filepath.Join(home, hostSkillsDir)
	if isFolder(skills) && !slices.ContainsFunc(mounts, func(m config.Mount) bool { return m.Touches(guestSkillsDir) }) {
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
