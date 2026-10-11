// Package gitconfig carries the git identity of the host into the VM.
package gitconfig

import (
	"os/exec"
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
