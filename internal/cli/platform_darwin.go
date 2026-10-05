package cli

import (
	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/applelaunch"
)

func newBackend() imageBackend { return applelaunch.NewBackend() }

// platformCommands are the commands only this kind of host has, and macOS
// has none.
func platformCommands() []*cli.Command { return nil }
