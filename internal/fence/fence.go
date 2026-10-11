// Package fence does the work of aibox in a folder the VM can write in a
// helper, aibox started again, that the kernel lets write only below one
// folder. A symlink the VM put there then leads no write out of it,
// whichever code or program follows it.
package fence

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// Name is the hidden command of aibox that runs the helper.
const Name = "fence"

const (
	execJob  = "exec"
	writeJob = "write"
)

// Command returns the command that runs the program in the helper, which
// may write only below dir, or nowhere but /dev/null when dir is empty.
// The program inherits that. It is used like exec.Command.
func Command(dir, name string, args ...string) *exec.Cmd {
	if !confines {
		return exec.Command(name, args...) //nolint:gosec // the caller's program
	}

	self, err := os.Executable()
	cmd := exec.Command(self, append([]string{Name, execJob, dir, name}, args...)...) //nolint:gosec // aibox starts itself with the caller's program
	if err != nil {
		cmd.Err = fmt.Errorf("find aibox for the helper: %w", err)
	}

	return cmd
}

// WriteFile writes a new file below dir in the helper, which may write
// only below dir. Whatever has the name goes first, so a symlink there is
// replaced, not followed.
func WriteFile(dir, name string, data []byte) error {
	if !confines {
		return writeFile(dir, name, bytes.NewReader(data))
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find aibox for the helper: %w", err)
	}

	cmd := exec.Command(self, Name, writeJob, dir, name) //nolint:gosec // aibox starts itself
	cmd.Stdin = bytes.NewReader(data)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
	}

	return nil
}

// writeFile stays inside dir with os.Root too, for a host where no helper
// confines it.
func writeFile(dir, name string, in io.Reader) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	if err := root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}

	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, in); err != nil {
		_ = f.Close()

		return err
	}

	return f.Close()
}
