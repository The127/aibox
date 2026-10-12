package cli

import (
	"encoding/json"
	"errors"
	"fmt"
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

// shareClaudeSetup puts the person's CLAUDE.md and their shared settings
// into the VM's home, as cfg allows. The VM writes that home, so the files
// go in through the helper and the VM's settings are read in a root, so
// that no link it left leads aibox outside.
func shareClaudeSetup(homeDir func() (string, error), vmHome string, cfg config.Config) error {
	home, err := homeDir()
	if err != nil {
		return fmt.Errorf("find the home folder: %w", err)
	}

	if cfg.ClaudeMD != config.ShareNone {
		content, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md")) //nolint:gosec // the person's own file
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return fmt.Errorf("read your CLAUDE.md: %w", err)
		default:
			if err := fence.WriteFile(vmHome, filepath.Join(".claude", "CLAUDE.md"), content); err != nil {
				return fmt.Errorf("share your CLAUDE.md: %w", err)
			}
		}
	}

	if cfg.Settings != config.ShareNone {
		if err := shareSettings(home, vmHome); err != nil {
			return fmt.Errorf("share your settings: %w", err)
		}
	}

	return nil
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
		if value, ok := mine[key]; ok {
			shared[key] = value
		}
	}

	if len(shared) == 0 {
		return nil
	}

	settings := vmSettings(vmHome)
	for key, value := range shared {
		settings[key] = value
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return fence.WriteFile(vmHome, filepath.Join(".claude", "settings.json"), append(out, '\n'))
}

// vmSettings are the settings the VM keeps in its home, or none when it
// keeps none it can read.
func vmSettings(vmHome string) map[string]json.RawMessage {
	settings := map[string]json.RawMessage{}

	root, err := os.OpenRoot(vmHome)
	if err != nil {
		return settings
	}

	defer func() { _ = root.Close() }()

	content, err := root.ReadFile(filepath.Join(".claude", "settings.json"))
	if err != nil || json.Unmarshal(content, &settings) != nil {
		return map[string]json.RawMessage{}
	}

	return settings
}
