// Package cli defines the aibox command line.
package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/version"
	"github.com/the127/aibox/internal/vm"
)

// dependencies are the host functions the commands call.
type dependencies struct {
	getwd    func() (string, error)
	aiboxDir func() (string, error)
	run      func(ctx context.Context, machine vm.Machine, options launch.Options) error
}

// NewRootCommand returns the top-level aibox command.
func NewRootCommand() *cli.Command {
	return newRootCommand(dependencies{
		getwd:    os.Getwd,
		aiboxDir: aiboxDir,
		run:      launch.Run,
	})
}

func newRootCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:     "aibox",
		Usage:    "run Claude Code inside a microVM",
		Version:  version.Get(),
		Commands: []*cli.Command{runCommand(deps)},
	}
}

func aiboxDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".aibox"), nil
}
