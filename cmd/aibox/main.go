// Command aibox runs Claude Code inside a microVM.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/the127/aibox/internal/cli"
)

// the exit code of a program ended by Ctrl-C
const exitInterrupted = 130

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := cli.NewRootCommand().Run(ctx, os.Args)
	if errors.Is(err, context.Canceled) {
		return exitInterrupted
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "aibox:", err)
	}

	return cli.ExitCode(err)
}
