package cli

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/the127/aibox/internal/fence"
	"github.com/the127/aibox/internal/ide"
)

// idePort is the port on the loopback of the VM where Claude Code reaches
// the IDE of the host through aibox.
const idePort = 3131

// errIDEPort is a port of the allow list that the IDE link takes on the
// loopback of the VM. With the IDE turned on it is refused whether an IDE
// is open or not, so that a config does not stop working once one is.
var errIDEPort = fmt.Errorf("allow names port %d of the loopback, which the VM needs for the IDE", idePort)

// errOwnSSEPort is a variable of env that aibox sets itself for the IDE.
var errOwnSSEPort = errors.New("env sets CLAUDE_CODE_SSE_PORT, which aibox sets itself for the IDE")

// checkIDE refuses a config that takes what the IDE link needs.
func checkIDE(loopback []uint16, env []string) error {
	if slices.Contains(loopback, idePort) {
		return errIDEPort
	}

	if hasVariable(env, "CLAUDE_CODE_SSE_PORT") {
		return errOwnSSEPort
	}

	return nil
}

// findIDE returns the IDE that has the project open, from the lock files
// in the folders where Claude Code looks for them, or false when there is
// none. Claude Code looks in the folder of CLAUDE_CONFIG_DIR and in that
// of the home.
func findIDE(deps dependencies, cwd string) (ide.IDE, bool, error) {
	var dirs []string

	if dir, ok := deps.lookupEnv("CLAUDE_CONFIG_DIR"); ok && dir != "" {
		dirs = append(dirs, filepath.Join(dir, "ide"))
	}

	home, err := deps.homeDir()
	if err != nil {
		return ide.IDE{}, false, fmt.Errorf("find the home directory: %w", err)
	}

	dirs = append(dirs, filepath.Join(home, ".claude", "ide"))

	for _, dir := range dirs {
		found, err := ide.Find(dir, cwd)
		if err != nil {
			return ide.IDE{}, false, err
		}

		if len(found) > 0 {
			return found[0], true, nil
		}
	}

	return ide.IDE{}, false, nil
}

// linkIDE lets Claude Code in the VM find the IDE: a lock file in the
// home of the VM names the port of the link and a token of its own, so
// that the token of the IDE stays on the host, and CLAUDE_CODE_SSE_PORT
// makes Claude Code connect by itself. The lock file names no process,
// since Claude Code would look for it in the VM. Without one it checks
// that the port answers. It returns the handler that serves the link.
func linkIDE(found ide.IDE, project, home string, log io.Writer) ([]string, http.Handler, error) {
	token := rand.Text()

	lock, err := json.Marshal(map[string]any{
		"workspaceFolders": []string{"/project"},
		"ideName":          found.Name,
		"transport":        "ws",
		"authToken":        token,
	})
	if err != nil {
		return nil, nil, err
	}

	// the VM can change its home, and a symlink it put there must not lead
	// the write to a file of the host
	if err := fence.WriteFile(home, filepath.Join(".claude", "ide", strconv.Itoa(idePort)+".lock"), lock); err != nil {
		return nil, nil, fmt.Errorf("write the lock file of the IDE: %w", err)
	}

	env := []string{"CLAUDE_CODE_SSE_PORT=" + strconv.Itoa(idePort)}

	return env, &ide.Bridge{IDE: found, Token: token, Project: project, Log: log}, nil
}
