package cli

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/project"
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

	machine := vm.Machine{
		Kernel:    kernel,
		Rootfs:    rootfs,
		MemoryMiB: cmd.Int("memory"),
		CPUs:      cmd.Int("cpus"),
		Shares: []vm.Share{
			{Tag: "project", Dir: cwd},
			{Tag: "home", Dir: p.Home},
		},
		GuestCID: randomCID(),
	}

	return deps.run(ctx, machine, launch.Options{
		QEMU:      qemuProgram,
		Virtiofsd: virtiofsdProgram,
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	})
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
