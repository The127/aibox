//go:build !linux && !(darwin && cgo)

package cli

import (
	"context"
	"errors"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/backend"
)

var errNoBackend = errors.New("aibox runs a VM on Linux, and on macOS when built with cgo")

type noBackend struct{}

func (noBackend) CheckImage(string) error                 { return errNoBackend }
func (noBackend) Run(context.Context, backend.Spec) error { return errNoBackend }

func newBackend() imageBackend { return noBackend{} }

func platformCommands() []*cli.Command { return nil }
