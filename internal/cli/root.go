// Package cli defines the aibox command line.
package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/gitconfig"
	"github.com/the127/aibox/internal/image"
	"github.com/the127/aibox/internal/version"
)

// dependencies are the host functions the commands call.
type dependencies struct {
	getwd           func() (string, error)
	aiboxDir        func() (string, error)
	homeDir         func() (string, error)
	uid             func() int
	stdinIsTerminal func() bool
	lookupEnv       func(name string) (string, bool)
	gitIdentity     func(dir string) gitconfig.Identity
	backend         backend.Backend
	edit            func(editor, path string) error
	version         func() string
	// fetchImage downloads the image of the release into dir
	fetchImage func(ctx context.Context, version, arch, dir string) error
}

// ExitCode is the code aibox ends with after the error: the code of the
// command in the VM when that failed, 1 for anything else, 0 for nil.
func ExitCode(err error) int {
	var exit *backend.ExitError
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
		uid:             os.Getuid,
		stdinIsTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		lookupEnv:       os.LookupEnv,
		gitIdentity:     gitconfig.Read,
		backend:         newBackend(),
		edit:            runEditor,
		version:         version.Get,
		fetchImage:      image.Fetcher{BaseURL: image.ReleasesURL, Client: http.DefaultClient}.Fetch,
	})
}

func newRootCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:     "aibox",
		Usage:    "run Claude Code inside a microVM",
		Version:  version.Get(),
		Commands: append([]*cli.Command{runCommand(deps), configCommand(deps)}, platformCommands()...),
	}
}

func aiboxDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".aibox"), nil
}
