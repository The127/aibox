package cli

import (
	"context"
	"crypto/x509"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/vsockns"
)

func newBackend() backend.Backend { return launch.NewBackend() }

// systemRoots are the root certificates of the host, read before aibox is
// confined and can read the files no longer.
func systemRoots() (*x509.CertPool, error) { return x509.SystemCertPool() }

// platformCommands are the commands only this kind of host has. The helper
// aibox run starts to make the vsock namespace of a VM is not for people to
// call.
func platformCommands() []*cli.Command {
	return []*cli.Command{{
		Name:   vsockns.Command,
		Hidden: true,
		Action: func(context.Context, *cli.Command) error { return vsockns.Serve() },
	}}
}
