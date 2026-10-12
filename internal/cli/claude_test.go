package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeFile writes a file into the person's ~/.claude.
func (f *fixture) claudeFile(t *testing.T, name, content string) {
	t.Helper()

	path := filepath.Join(f.homeDir, ".claude", name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func (f *fixture) runWithImage(t *testing.T) error {
	t.Helper()

	return f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))
}

func TestRunSharesTheAgentsAndCommands(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.claudeFile(t, "agents/reviewer.md", "x")
	f.claudeFile(t, "commands/ship.md", "x")

	// act
	err := f.runWithImage(t)

	// assert
	require.NoError(t, err)
	assert.Contains(t, guests(f.launch.spec.Mounts), "/home/user/.claude/agents")
	assert.Contains(t, guests(f.launch.spec.Mounts), "/home/user/.claude/commands")
}

func TestRunSharesNoAgentsOrCommandsWithNone(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.claudeFile(t, "agents/reviewer.md", "x")
	f.claudeFile(t, "commands/ship.md", "x")
	f.writeConfig(t, "allow:\n  - api.anthropic.com\nagents: none\ncommands: none\n")

	// act
	err := f.runWithImage(t)

	// assert
	require.NoError(t, err)
	assert.NotContains(t, guests(f.launch.spec.Mounts), "/home/user/.claude/agents")
	assert.NotContains(t, guests(f.launch.spec.Mounts), "/home/user/.claude/commands")
}

func TestRunPutsTheClaudeMDIntoTheHome(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.claudeFile(t, "CLAUDE.md", "Use short sentences.\n")

	// act
	err := f.runWithImage(t)

	// assert
	require.NoError(t, err)

	written, err := os.ReadFile(filepath.Join(f.project(t).Home, ".claude", "CLAUDE.md"))
	require.NoError(t, err)
	assert.Equal(t, "Use short sentences.\n", string(written))
}

func TestRunPutsNoClaudeMDIntoTheHomeWithNone(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.claudeFile(t, "CLAUDE.md", "Use short sentences.\n")
	f.writeConfig(t, "allow:\n  - api.anthropic.com\nclaude-md: none\n")

	// act
	err := f.runWithImage(t)

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(f.project(t).Home, ".claude", "CLAUDE.md"))
}

func TestRunSharesOnlyTheSafeSettings(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.claudeFile(t, "settings.json", `{"model": "opus", "theme": "dark", "hooks": {"Stop": []}, "env": {"TOKEN": "secret"}, "apiKeyHelper": "/bin/key"}`)

	vmSettings := filepath.Join(f.project(t).Home, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(vmSettings), 0o700))
	require.NoError(t, os.WriteFile(vmSettings, []byte(`{"theme": "light", "verbose": true}`), 0o600))

	// act
	err := f.runWithImage(t)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(vmSettings) //nolint:gosec // a file in the test
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(content, &settings))
	assert.Equal(t, map[string]any{"model": "opus", "theme": "dark", "verbose": true}, settings, "yours win, the VM's own stay, the rest stays out")
}

func TestRunSharesNoSettingsWithNone(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.claudeFile(t, "settings.json", `{"model": "opus"}`)
	f.writeConfig(t, "allow:\n  - api.anthropic.com\nsettings: none\n")

	// act
	err := f.runWithImage(t)

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(f.project(t).Home, ".claude", "settings.json"))
}

func TestRunFollowsNoLinkTheVMLeftForTheSettings(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.claudeFile(t, "settings.json", `{"model": "opus"}`)

	outside := filepath.Join(t.TempDir(), "secret.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"token": "x"}`), 0o600))

	vmSettings := filepath.Join(f.project(t).Home, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(vmSettings), 0o700))
	symlink(t, outside, vmSettings)

	// act
	err := f.runWithImage(t)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(vmSettings) //nolint:gosec // a file in the test
	require.NoError(t, err)
	assert.JSONEq(t, `{"model": "opus"}`, string(content), "nothing of the file outside comes in")

	untouched, err := os.ReadFile(outside) //nolint:gosec // a temp file of the test
	require.NoError(t, err)
	assert.JSONEq(t, `{"token": "x"}`, string(untouched))
}
