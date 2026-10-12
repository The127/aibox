package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/hvinstall"
	"github.com/the127/aibox/internal/hvserver"
	"github.com/the127/aibox/internal/hvshares"
)

// platformCommands are the commands only this kind of host has: the
// service, which makes the VMs, as Hyper-V serves only Administrators.
// The command Windows' service manager runs it by is not for people to
// call.
func platformCommands(deps dependencies) []*cli.Command {
	return []*cli.Command{{
		Name:  "service",
		Usage: "install or uninstall the Windows service that makes the VMs of aibox",
		Commands: []*cli.Command{{
			Name:  "install",
			Usage: "install the service, or bring it up to date, from an elevated prompt",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "image", Usage: "folder with the image of the VM", DefaultText: "the image of this release"},
			},
			Action: func(ctx context.Context, cmd *cli.Command) error {
				if err := hvinstall.CheckAdministrator(); err != nil {
					return err
				}

				image := cmd.String("image")
				if image == "" {
					aibox, err := deps.aiboxDir()
					if err != nil {
						return fmt.Errorf("find the aibox folder: %w", err)
					}

					if image, err = defaultImage(ctx, deps, filepath.Join(aibox, "image")); err != nil {
						return err
					}
				}

				return hvinstall.Install(image)
			},
		}, {
			Name:  "uninstall",
			Usage: "stop and remove the service, from an elevated prompt",
			Action: func(context.Context, *cli.Command) error {
				err := hvinstall.Uninstall()
				if errors.Is(err, hvinstall.ErrAtRestart) {
					_, _ = fmt.Fprintf(deps.stderr, "aibox: the service is uninstalled; %v\n", err)

					return nil
				}

				return err
			},
		}, {
			Name:   "run",
			Hidden: true,
			Action: func(context.Context, *cli.Command) error { return hvserver.Run() },
		}},
	}, {
		Name:   hvshares.Command,
		Hidden: true,
		Action: func(context.Context, *cli.Command) error { return hvshares.Serve(os.Stdin, os.Stdout) },
	}}
}
