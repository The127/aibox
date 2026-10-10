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
	// gitLogin is the login git on the host has for a remote
	gitLogin func(ctx context.Context, remote string) (gitconfig.Login, error)
	// sshKeys are the SSH keys and the known hosts of the host
	sshKeys func() (sshKeys, error)
	backend backend.Backend
	edit    func(editor, path string) error
	version func() string
	// imageDigest is the digest of the image this aibox was released with
	imageDigest func(arch string) (string, bool)
	// kernelDigest is the digest of the kernel this aibox was released with
	kernelDigest func(arch string) (string, bool)
	// systemImage is the folder where a package puts the image
	systemImage string
	// fetchImage downloads the image of the release into dir
	fetchImage func(ctx context.Context, version, arch, digest, dir string) error
	// stdout and stderr are where a task reports
	stdout, stderr io.Writer
}

// sshKeys say how the host reaches a git server over SSH, until Close.
type sshKeys interface {
	For(ctx context.Context, host string) (gitconfig.SSHTarget, error)
	Close()
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

// printedError is an error the command printed itself.
type printedError struct{ error }

func (e printedError) Unwrap() error { return e.error }

// Printed reports whether the command printed the error itself already,
// so that it is not printed once more.
func Printed(err error) bool {
	var printed printedError

	return errors.As(err, &printed)
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
		gitLogin:        gitconfig.LoginFor,
		sshKeys:         func() (sshKeys, error) { return gitconfig.LoadSSHKeys() },
		backend:         newBackend(),
		edit:            runEditor,
		version:         version.Get,
		imageDigest:     image.Digest,
		kernelDigest:    image.KernelDigest,
		systemImage:     image.SystemDir(),
		fetchImage:      fetchImage(os.Stderr),
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

// fetchImage downloads images from the releases and shows the progress on
// w.
func fetchImage(w io.Writer) func(ctx context.Context, version, arch, digest, dir string) error {
	show, end := showProgress(w)
	fetcher := image.Fetcher{BaseURL: image.ReleasesURL, Client: image.NewClient(), Progress: show}

	return func(ctx context.Context, version, arch, digest, dir string) error {
		defer end()

		return fetcher.Fetch(ctx, version, arch, digest, dir)
	}
}

// showProgress writes how much of a download arrived, in MB, on one line
// that it rewrites when the count changes. end ends the line once the
// download is over, also when it failed or its size was not known, so that
// what follows starts on a line of its own.
func showProgress(w io.Writer) (show func(done, total int64), end func()) {
	shown := int64(-1)

	show = func(done, total int64) {
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
	}

	end = func() {
		if shown >= 0 {
			_, _ = fmt.Fprintln(w)
		}

		shown = -1
	}

	return show, end
}
