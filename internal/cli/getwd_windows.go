package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/the127/aibox/internal/realpath"
)

// getwd is the current folder as the file system spells it. Windows keeps
// the case that was typed, c:\users\me\repo or C:\Users\me\repo, and the
// folder of the project is named after the path, so each spelling would
// get a project of its own: its own config, login and state disk.
//
// A folder that can't be opened, or that is on a volume with no drive,
// which Windows names \\?\Volume{...} and git and other programs don't
// take for a folder to run in, stays as it was typed: it is still the
// project.
func getwd() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	if resolved, err := realpath.Of(cwd); err == nil && !strings.HasPrefix(filepath.VolumeName(resolved), `\\?\`) {
		return resolved, nil
	}

	return cwd, nil
}
