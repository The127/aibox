// Package cli defines the aibox command line.
package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/gitconfig"
	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/version"
	"github.com/the127/aibox/internal/vm"
)

// dependencies are the host functions the commands call.
type dependencies struct {
	getwd       func() (string, error)
	aiboxDir    func() (string, error)
	homeDir     func() (string, error)
	owner       func() vm.Owner
	lookupEnv   func(name string) (string, bool)
	gitIdentity func(dir string) gitconfig.Identity
	run         func(ctx context.Context, machine vm.Machine, options launch.Options) error
}

// NewRootCommand returns the top-level aibox command.
func NewRootCommand() *cli.Command {
	return newRootCommand(dependencies{
		getwd:       os.Getwd,
		aiboxDir:    aiboxDir,
		homeDir:     os.UserHomeDir,
		owner:       owner,
		lookupEnv:   os.LookupEnv,
		gitIdentity: gitconfig.Read,
		run:         launch.Run,
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

func owner() vm.Owner {
	return vm.Owner{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())} //nolint:gosec // never negative on Linux
}

func aiboxDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".aibox"), nil
}
