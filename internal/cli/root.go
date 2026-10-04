// Package cli defines the aibox command line.
package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/the127/aibox/internal/gitconfig"
	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/version"
	"github.com/the127/aibox/internal/vm"
	"github.com/the127/aibox/internal/vsockns"
)

// dependencies are the host functions the commands call.
type dependencies struct {
	getwd           func() (string, error)
	aiboxDir        func() (string, error)
	homeDir         func() (string, error)
	owner           func() vm.Owner
	stdinIsTerminal func() bool
	lookupEnv       func(name string) (string, bool)
	gitIdentity     func(dir string) gitconfig.Identity
	run             func(ctx context.Context, machine vm.Machine, options launch.Options) error
	edit            func(editor, path string) error
}

// ExitCode is the code aibox ends with after the error: the code of the
// command in the VM when that failed, 1 for anything else, 0 for nil.
func ExitCode(err error) int {
	var exit *launch.ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}

	if err != nil {
		return 1
	}

	return 0
}

// NewRootCommand returns the top-level aibox command.
func NewRootCommand() *cli.Command {
	return newRootCommand(dependencies{
		getwd:           os.Getwd,
		aiboxDir:        aiboxDir,
		homeDir:         os.UserHomeDir,
		owner:           owner,
		stdinIsTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		lookupEnv:       os.LookupEnv,
		gitIdentity:     gitconfig.Read,
		run:             launch.Run,
		edit:            runEditor,
	})
}

func newRootCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:     "aibox",
		Usage:    "run Claude Code inside a microVM",
		Version:  version.Get(),
		Commands: []*cli.Command{runCommand(deps), configCommand(deps), namespaceCommand()},
	}
}

// namespaceCommand is the helper aibox run starts to make the vsock
// namespace of a VM. It is not for people to call.
func namespaceCommand() *cli.Command {
	return &cli.Command{
		Name:   vsockns.Command,
		Hidden: true,
		Action: func(context.Context, *cli.Command) error { return vsockns.Serve() },
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
