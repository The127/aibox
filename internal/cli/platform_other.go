//go:build !linux && !windows && !(darwin && cgo)

package cli

import (
	"context"
	"crypto/x509"
	"errors"

	"github.com/the127/aibox/internal/backend"
)

var errNoBackend = errors.New("aibox runs a VM on Linux, on Windows, and on macOS when built with cgo")

type noBackend struct{}

func (noBackend) Run(context.Context, backend.Spec) error { return errNoBackend }

func newBackend() backend.Backend { return noBackend{} }

func systemRoots() (*x509.CertPool, error) { return x509.SystemCertPool() }
