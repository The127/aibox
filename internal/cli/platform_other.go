//go:build !linux && !(darwin && cgo)

package cli

import (
	"context"
	"crypto/x509"
	"errors"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/backend"
)

var errNoBackend = errors.New("aibox runs a VM on Linux, and on macOS when built with cgo")

type noBackend struct{}

func (noBackend) Run(context.Context, backend.Spec) error { return errNoBackend }

func newBackend() backend.Backend { return noBackend{} }

func systemRoots() (*x509.CertPool, error) { return x509.SystemCertPool() }

func platformCommands() []*cli.Command { return nil }
