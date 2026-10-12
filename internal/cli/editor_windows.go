package cli

import (
	"os"
	"os/exec"
	"syscall"
)

// fallbackEditor is the editor every Windows has, where vi, git's, is
// only in the shell of Git for Windows.
const fallbackEditor = "notepad"

// runEditor runs the editor on the file through cmd, as the shell does
// elsewhere, so that an editor setting with arguments works, and waits for
// it whatever happens to aibox meanwhile, because the person is in the
// editor. cmd takes its command line as it is, not split into arguments:
// /s makes it drop the outer quotes, and a path on Windows has no quote
// that could end the one around it.
func runEditor(editor, path string) error {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + editor + ` "` + path + `""`}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
