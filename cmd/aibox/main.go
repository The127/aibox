// Command aibox runs Claude Code inside a microVM.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/the127/aibox/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
