package cli

import (
	"crypto/x509"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/hvlaunch"
	"github.com/the127/aibox/internal/version"
)

func newBackend() backend.Backend { return hvlaunch.NewBackend(version.Get()) }

func systemRoots() (*x509.CertPool, error) { return x509.SystemCertPool() }
