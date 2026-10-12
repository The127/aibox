// Package gitconfig carries the git identity and settings of the host into
// the VM.
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

// AutoCRLF returns how git converts line endings in dir on the host, the
// value of core.autocrlf, or empty when git has none or is not installed.
// Git for Windows sets it in the config of the system, which the VM has
// no copy of.
func AutoCRLF(dir string, command func(name string, args ...string) *exec.Cmd) string {
	return get(dir, "core.autocrlf", command)
}

func get(dir, key string, command func(name string, args ...string) *exec.Cmd) string {
	out, err := command("git", "-C", dir, "config", "--get", key).Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}
