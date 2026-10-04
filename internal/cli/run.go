package cli

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/gitconfig"
	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/project"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/vm"
)

const (
	qemuProgram      = "qemu-system-x86_64"
	virtiofsdProgram = "/usr/libexec/virtiofsd"
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

	owner := deps.owner()

	machine := vm.Machine{
		Kernel:    kernel,
		Rootfs:    rootfs,
		MemoryMiB: flagOrConfig(cmd, "memory", cfg.Memory),
		CPUs:      flagOrConfig(cmd, "cpus", cfg.CPUs),
		Shares: []vm.Share{
			{Tag: "project", Dir: cwd},
			{Tag: "home", Dir: p.Home},
		},
		Owner:      &owner,
		GuestCID:   randomCID(),
		Shell:      cmd.Bool("shell"),
		ConsoleLog: p.ConsoleLog,
	}

	return deps.run(ctx, machine, launchOptions(cfg, p, log))
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
