//go:build darwin && cgo

package cli

import (
	"crypto/x509"
	"errors"
	"os/exec"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/vzlaunch"
)

func newBackend() backend.Backend { return vzlaunch.NewBackend() }

// platformCommands are the commands only this kind of host has, and macOS
// has none.
func platformCommands() []*cli.Command { return nil }

// systemRoots are the root certificates of macOS and those an admin added
// to the system keychain, such as the root of a company proxy. The sandbox
// of aibox keeps macOS from checking certificates itself, so Go checks
// them with these, read before.
func systemRoots() (*x509.CertPool, error) {
	roots := x509.NewCertPool()

	for _, keychain := range []string{"/System/Library/Keychains/SystemRootCertificates.keychain", "/Library/Keychains/System.keychain"} {
		// a system keychain without certificates is no reason to stop
		pem, _ := exec.Command("/usr/bin/security", "find-certificate", "-a", "-p", keychain).Output() //nolint:gosec // the keychains are fixed paths
		roots.AppendCertsFromPEM(pem)
	}

	if len(roots.Subjects()) == 0 { //nolint:staticcheck // only counts the roots
		return nil, errNoRoots
	}

	return roots, nil
}

var errNoRoots = errors.New("macOS gave no root certificates")
