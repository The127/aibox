package gitconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrNoLogin is a remote git on the host has no login for.
var ErrNoLogin = errors.New("git on this machine has no login for it")

// Login is the user name and password, or token, git on the host uses for
// a remote.
type Login struct {
	Username string
	Password string
}

// loginTimeout is how long a credential helper may take, which may ask the
// person in a window of its own.
const loginTimeout = 2 * time.Minute

// LoginFor asks git on the host for its login for the HTTPS remote, given
// as host/path, as git push would. It runs outside any repository, so the
// config of a project, which the VM can write, has no say, and never asks
// on the terminal.
func LoginFor(ctx context.Context, remote string) (Login, error) {
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	host, repository, _ := strings.Cut(remote, "/")

	cmd := exec.CommandContext(ctx, "git", "credential", "fill")
	cmd.Dir = "/"
	cmd.Env = append(withoutRepository(os.Environ()), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("protocol=https\nhost=%s\npath=%s.git\n\n", host, repository))

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return Login{}, fmt.Errorf("ask git for the login of https://%s: %w", remote, err)
	}

	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return Login{}, fmt.Errorf("https://%s: %w: %s", remote, ErrNoLogin, message)
		}

		return Login{}, fmt.Errorf("https://%s: %w", remote, ErrNoLogin)
	}

	var login Login

	for line := range strings.SplitSeq(string(out), "\n") {
		key, value, _ := strings.Cut(line, "=")

		switch key {
		case "username":
			login.Username = value
		case "password":
			login.Password = value
		}
	}

	if login.Password == "" {
		return Login{}, fmt.Errorf("https://%s: %w", remote, ErrNoLogin)
	}

	return login, nil
}

// withoutRepository drops the variables that point git at a repository.
func withoutRepository(env []string) []string {
	var kept []string

	for _, variable := range env {
		name, _, _ := strings.Cut(variable, "=")
		if name == "GIT_DIR" || name == "GIT_WORK_TREE" || name == "GIT_COMMON_DIR" || name == "GIT_INDEX_FILE" || name == "GIT_OBJECT_DIRECTORY" {
			continue
		}

		kept = append(kept, variable)
	}

	return kept
}
