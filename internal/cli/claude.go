package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/fence"
)

// sharedSettings are the keys of the person's settings.json the VM gets:
// preferences that start no program and hold no secret. hooks, env,
// apiKeyHelper, statusLine, permissions and the MCP servers stay out.
var sharedSettings = []string{"model", "theme", "attribution", "includeCoAuthoredBy", "outputStyle", "alwaysThinkingEnabled"}

// maxVMSettings is the most of the VM's settings.json aibox reads. Claude
// Code writes a few KiB there, and the VM could leave a file of any size.
const maxVMSettings = 1 << 20

// shareClaudeSetup puts the person's CLAUDE.md and their shared settings
// into the VM's home, as cfg allows. The VM writes that home, so the files
// go in through the helper and the VM's settings are read in a root, so
// that no link it left leads aibox outside. What the VM left there can
// keep a file from going in, but not the VM from starting, so a failure is
// a warning.
func shareClaudeSetup(home, vmHome string, cfg config.Config, stderr io.Writer) {
	warn := func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, "aibox: "+format+"\n", args...)
	}

	if cfg.ClaudeMD != config.ShareNone {
		content, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md")) //nolint:gosec // the person's own file

		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			warn("the VM gets no CLAUDE.md: %v", err)
		default:
			if err := fence.WriteFile(vmHome, filepath.Join(".claude", "CLAUDE.md"), content); err != nil {
				warn("the VM gets no CLAUDE.md: %v", err)
			}
		}
	}

	if cfg.Settings != config.ShareNone {
		if err := shareSettings(home, vmHome); err != nil {
			warn("the VM gets none of your settings: %v", err)
		}
	}
}

// shareSettings sets the shared settings of the person in the VM's
// settings.json, and keeps the VM's other settings.
func shareSettings(home, vmHome string) error {
	content, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json")) //nolint:gosec // the person's own file
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	var mine map[string]json.RawMessage
	if err := json.Unmarshal(content, &mine); err != nil {
		return fmt.Errorf("read ~/.claude/settings.json: %w", err)
	}

	shared := map[string]json.RawMessage{}

	for _, key := range sharedSettings {
		if value, ok := mine[key]; ok && string(value) != "null" {
			shared[key] = value
		}
	}

	if len(shared) == 0 {
		return nil
	}

	settings, err := vmSettings(vmHome)
	if err != nil {
		return fmt.Errorf("the VM's settings.json, which stays as it is: %w", err)
	}

	for key, value := range shared {
		settings[key] = value
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return fence.WriteFile(vmHome, filepath.Join(".claude", "settings.json"), append(out, '\n'))
}

var errNoSettingsFile = errors.New("not a plain file of settings")

// vmSettings are the settings the VM keeps in its home, none when it keeps
// none, or an error when its file is no plain file of JSON settings.
func vmSettings(vmHome string) (map[string]json.RawMessage, error) {
	root, err := os.OpenRoot(vmHome)
	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	name := filepath.Join(".claude", "settings.json")

	// without waiting, since the VM could have left a FIFO there
	file, err := openInRoot(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}

	if err != nil {
		return nil, err
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() || info.Size() > maxVMSettings {
		return nil, errNoSettingsFile
	}

	content, err := io.ReadAll(io.LimitReader(file, maxVMSettings))
	if err != nil {
		return nil, err
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(content, &settings); err != nil || settings == nil {
		return nil, errNoSettingsFile
	}

	return settings, nil
}
