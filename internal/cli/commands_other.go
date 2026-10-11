//go:build !linux && !windows && !(darwin && cgo)

package cli

import "github.com/urfave/cli/v3"

func platformCommands(dependencies) []*cli.Command { return nil }
