package cli

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/clipboard"
	"github.com/the127/aibox/internal/vm"
)

// errClipboardPort is a port of the allow list that the clipboard takes on
// the loopback of the VM.
var errClipboardPort = fmt.Errorf("allow names port %d of the loopback, which the VM needs for the clipboard, unless you set clipboard: false", vm.ClipboardPort)

// clipboardCommand is the helper aibox run starts to read the clipboard,
// which is not for people to call.
func clipboardCommand() *cli.Command {
	return &cli.Command{
		Name:   clipboard.Command,
		Hidden: true,
		Action: func(context.Context, *cli.Command) error { return clipboard.Serve(os.Stdin, os.Stdout) },
	}
}

// clipboardHelper reads the clipboard of the host until it is closed.
type clipboardHelper interface {
	Read() ([]byte, error)
	Close() error
}

// shareClipboard starts the helper that reads the clipboard, while aibox
// may still start programs, and returns the gate the VM reads it through.
// It refuses a config that takes the port of the clipboard.
func shareClipboard(deps dependencies, loopback []uint16, log *os.File) (*clipboard.Gate, clipboardHelper, error) {
	if slices.Contains(loopback, vm.ClipboardPort) {
		return nil, nil, errClipboardPort
	}

	helper, err := deps.startClipboard(log)
	if err != nil {
		return nil, nil, err
	}

	return &clipboard.Gate{Read: helper.Read, Log: log}, helper, nil
}
