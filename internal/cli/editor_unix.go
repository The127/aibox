//go:build !windows

package cli

import (
	"os"
	"os/exec"
)

// fallbackEditor is the editor git falls back to, too.
const fallbackEditor = "vi"

// runEditor runs the editor on the file through the shell, so that an
// editor setting with arguments works, and waits for it whatever happens to
// aibox meanwhile, because the person is in the editor.
func runEditor(editor, path string) error {
	cmd := exec.Command("/bin/sh", "-c", editor+` "$0"`, path) //nolint:gosec // the editor is the choice of the person
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
