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
// empty one when git has none or is not installed. command makes the
// command that runs git, as exec.Command does.
func Read(dir string, command func(name string, args ...string) *exec.Cmd) Identity {
	return Identity{Name: get(dir, "user.name", command), Email: get(dir, "user.email", command)}
}

func get(dir, key string, command func(name string, args ...string) *exec.Cmd) string {
	out, err := command("git", "-C", dir, "config", "--get", key).Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}
