// Package cli defines the aibox command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	// stdin is where a task reads its prompt from
	stdin       io.Reader
	lookupEnv   func(name string) (string, bool)
	gitIdentity func(dir string) gitconfig.Identity
	backend     backend.Backend
	edit        func(editor, path string) error
	version     func() string
	// imageDigest is the digest of the image this aibox was released with
	imageDigest func(arch string) (string, bool)
	// fetchImage downloads the image of the release into dir
	fetchImage func(ctx context.Context, version, arch, digest, dir string) error
	// stdout and stderr are where a task reports
	stdout, stderr io.Writer
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
		stdin:           os.Stdin,
		lookupEnv:       os.LookupEnv,
		gitIdentity:     gitconfig.Read,
		backend:         newBackend(),
		edit:            runEditor,
		version:         version.Get,
		imageDigest:     image.Digest,
		fetchImage:      image.Fetcher{BaseURL: image.ReleasesURL, Client: image.NewClient(), Progress: showProgress(os.Stderr)}.Fetch,
		stdout:          os.Stdout,
		stderr:          os.Stderr,
	})
}

func newRootCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:     "aibox",
		Usage:    "run Claude Code inside a microVM",
		Version:  version.Get(),
		Commands: append([]*cli.Command{runCommand(deps), taskCommand(deps), tasksCommand(deps), configCommand(deps)}, platformCommands()...),
	}
}

func aiboxDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".aibox"), nil
}

// showProgress writes how much of a download arrived, in MB, on one line
// that it rewrites when the count changes.
func showProgress(w io.Writer) func(done, total int64) {
	shown := int64(-1)

	return func(done, total int64) {
		mb := done >> 20
		if mb == shown {
			return
		}

		shown = mb

		if total > 0 {
			_, _ = fmt.Fprintf(w, "\raibox: %d of %d MB", mb, total>>20)
		} else {
			_, _ = fmt.Fprintf(w, "\raibox: %d MB", mb)
		}

		if done == total {
			_, _ = fmt.Fprintln(w)
		}
	}
}
