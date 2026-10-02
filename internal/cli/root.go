// Package cli defines the aibox command line.
package cli

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/version"
)

// NewRootCommand returns the top-level aibox command.
func NewRootCommand() *cli.Command {
	return &cli.Command{
		Name:    "aibox",
		Usage:   "run Claude Code inside a microVM",
		Version: version.Get(),
		Action: func(_ context.Context, _ *cli.Command) error {
			fmt.Println("hello world")
			return nil
		},
	}
}
