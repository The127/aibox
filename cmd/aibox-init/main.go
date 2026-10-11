//go:build linux

// Command aibox-init is the first process of the aibox VM. Started under the
// name xclip or wl-paste it is the clipboard of the VM instead.
package main

import (
	"fmt"
	"os"

	"github.com/the127/aibox/internal/guest"
)

func main() {
	if guest.IsClipboardTool(os.Args) {
		os.Exit(guest.ClipboardTool(os.Args, os.Stdout, os.Stderr))
	}

	// Run powers the VM off itself. If that failed too, exiting PID 1 panics
	// the kernel, which with panic=-1 on the command line ends the VM as well.
	if err := guest.Run(guest.Linux{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
