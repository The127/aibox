package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/gitconfig"
	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/project"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/vm"
)

// QEMU, virtiofsd and the proxy would run with root rights on the host, and
// nothing here needs them.
var errRoot = errors.New("aibox must not run as root, start it as a normal user")

const (
	qemuProgram      = "qemu-system-x86_64"
	virtiofsdProgram = "/usr/libexec/virtiofsd"
	// the tags of the shares for the mounts of the config, followed by their
	// position
	mountTagPrefix = "mount"
)

// The skills of the person are shared from their home on the host into the
// home of the VM.
const (
	skillsTag      = "skills"
	hostSkillsDir  = ".claude/skills"
	guestSkillsDir = "/home/user/.claude/skills"
)

func runCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:  "run",
		Usage: "boot the VM with the current folder shared into it",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "image", Usage: "folder with vmlinuz and os.ext4", DefaultText: "~/.aibox/image"},
			&cli.IntFlag{Name: "memory", Usage: "memory of the VM in MiB", Value: 2048},
			&cli.IntFlag{Name: "cpus", Usage: "number of CPUs of the VM", Value: 2},
			&cli.BoolFlag{Name: "shell", Usage: "open a shell in the VM instead of Claude Code"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return run(ctx, deps, cmd)
		},
	}
}

func run(ctx context.Context, deps dependencies, cmd *cli.Command) error {
	owner := deps.owner()
	if owner.UID == 0 {
		return errRoot
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

	image := cmd.String("image")
	if image == "" {
		image = filepath.Join(aibox, "image")
	}

	kernel, rootfs, err := imageFiles(image)
	if err != nil {
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

	mounts, err := mountShares(deps.homeDir, cfg.Mounts)
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

	machine := vm.Machine{
		Kernel:    kernel,
		Rootfs:    rootfs,
		MemoryMiB: flagOrConfig(cmd, "memory", cfg.Memory),
		CPUs:      flagOrConfig(cmd, "cpus", cfg.CPUs),
		Shares: append([]vm.Share{
			{Tag: "project", Dir: cwd},
			{Tag: "home", Dir: p.Home},
		}, mounts...),
		Owner:      &owner,
		GuestCID:   randomCID(),
		Shell:      cmd.Bool("shell"),
		ConsoleLog: p.ConsoleLog,
	}

	options := launchOptions(cfg, p, log)
	options.Env = env

	return deps.run(ctx, machine, options)
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

// mountShares returns the share of the skills of the person, when the host
// has any and no mount of the config takes their place, and a share for
// each mount of the config, once the host folders are known to exist.
func mountShares(homeDir func() (string, error), mounts []config.Mount) ([]vm.Share, error) {
	home, err := homeDir()
	if err != nil {
		return nil, fmt.Errorf("find the home folder: %w", err)
	}

	shares := make([]vm.Share, 0, len(mounts)+1)

	// a person without skills is the normal case, so a missing folder is
	// nothing to report
	skills := filepath.Join(home, hostSkillsDir)
	if isFolder(skills) && !slices.ContainsFunc(mounts, func(m config.Mount) bool { return m.Touches(guestSkillsDir) }) {
		shares = append(shares, vm.Share{Tag: skillsTag, Dir: skills, Guest: guestSkillsDir})
	}

	for i, mount := range mounts {
		info, err := os.Stat(mount.Host)
		if err != nil {
			return nil, fmt.Errorf("mount %s: %w", mount.Guest, err)
		}

		if !info.IsDir() {
			return nil, fmt.Errorf("mount %s: %s is not a folder", mount.Guest, mount.Host)
		}

		shares = append(shares, vm.Share{Tag: fmt.Sprintf("%s%d", mountTagPrefix, i), Dir: mount.Host, Guest: mount.Guest})
	}

	return shares, nil
}

func isFolder(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

func launchOptions(cfg config.Config, p project.Project, log io.Writer) launch.Options {
	return launch.Options{
		QEMU:      qemuProgram,
		Virtiofsd: virtiofsdProgram,
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Proxy: proxy.Options{
			Allow:     cfg.Allow.Allows,
			OnRefused: proxy.RefusalLog(log),
			Hint:      "Add it to " + p.Config + " to allow it.",
		},
	}
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

func imageFiles(dir string) (kernel, rootfs string, err error) {
	kernel = filepath.Join(dir, "vmlinuz")
	rootfs = filepath.Join(dir, "os.ext4")

	for _, file := range []string{kernel, rootfs} {
		if _, err := os.Stat(file); err != nil {
			return "", "", fmt.Errorf("%w, build the VM image with just install-image or pass --image", err)
		}
	}

	return kernel, rootfs, nil
}

// randomCID picks a vsock context ID, so that two VMs on the host do not
// claim the same one. vsock reserves the IDs below 3.
func randomCID() uint32 {
	return 3 + rand.Uint32N(1<<31) //nolint:gosec // the ID only has to differ between VMs
}
