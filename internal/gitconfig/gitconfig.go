// Package gitconfig carries the git identity of the host into the VM.
package gitconfig

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Identity is the name and email git uses for commits.
type Identity struct {
	Name  string
	Email string
}

// Read returns the identity git uses for commits in dir on the host, or an
// empty one when git has none or is not installed.
func Read(dir string) Identity {
	return Identity{Name: get(dir, "user.name"), Email: get(dir, "user.email")}
}

func get(dir, key string) string {
	out, err := exec.Command("git", "-C", dir, "config", "--get", key).Output() //nolint:gosec // the keys are aibox's own constants
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}

// Write puts the identity into the git config of the home folder. A key
// without a value is removed. Other settings in the file stay.
func Write(home string, identity Identity) error {
	dir := filepath.Join(home, ".config", "git")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	// git reads this file before ~/.gitconfig, so a ~/.gitconfig made in the
	// VM wins
	file := filepath.Join(dir, "config")

	for key, value := range map[string]string{"user.name": identity.Name, "user.email": identity.Email} {
		if err := set(file, key, value); err != nil {
			return fmt.Errorf("write %s to %s: %w", key, file, err)
		}
	}

	return nil
}

// set lets git write the value, so that the quoting is git's own.
func set(file, key, value string) error {
	args := []string{"config", "-f", file, key, value}
	if value == "" {
		args = []string{"config", "-f", file, "--unset", key}
	}

	err := exec.Command("git", args...).Run() //nolint:gosec // the file and the keys are aibox's own

	// git exits with 5 when there is nothing to unset
	var exit *exec.ExitError
	if value == "" && errors.As(err, &exit) && exit.ExitCode() == 5 {
		return nil
	}

	return err
}
