//go:build linux

// Command aibox-init is the first process of the aibox VM.
package main

import (
	"fmt"
	"os"

	"github.com/the127/aibox/internal/guest"
)

func main() {
	// Run powers the VM off itself. If that failed too, exiting PID 1 panics
	// the kernel, which with panic=-1 on the command line ends the VM as well.
	if err := guest.Run(guest.Linux{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
