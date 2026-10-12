package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *fixture) suggest(answer string) error {
	f.deps.stdin = strings.NewReader(answer)

	return newRootCommand(f.deps).Run(context.Background(), []string{"aibox", "config", "suggest"})
}

func (f *fixture) projectFile(t *testing.T, name string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(f.cwd, name), []byte("x"), 0o600))
}

func (f *fixture) config(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(f.project(t).Config)
	require.NoError(t, err)

	return string(content)
}

func TestSuggestAddsThePresetsOnceConfirmed(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "go.mod")
	f.projectFile(t, "uv.lock")
	f.projectFile(t, "README.md")

	// act
	err := f.suggest("y\n")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "preset:go, for go.mod, allows proxy.golang.org, ")
	assert.Contains(t, f.stderr.String(), "preset:pypi, for uv.lock, allows pypi.org, files.pythonhosted.org\n")
	assert.Contains(t, f.stderr.String(), "Anyone can upload files")
	assert.Contains(t, f.stderr.String(), "Start aibox again")
	assert.Contains(t, f.config(t), "  - code.claude.com           # documentation lookups\n  - preset:go\n  - preset:pypi\n")

	cfg, err := loadConfig(f.deps, f.project(t).Config, f.aiboxDir)
	require.NoError(t, err)
	assert.True(t, cfg.Allow.Allows("proxy.golang.org", "443"))

	// and has nothing more to suggest after
	f.stderr.Reset()
	require.NoError(t, f.suggest("y\n"))
	assert.Contains(t, f.stderr.String(), "aibox: nothing to suggest")
}

func TestSuggestChangesNothingUnlessConfirmed(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "package.json")

	// act
	err := f.suggest("n\n")

	// assert
	require.ErrorIs(t, err, errNotConfirmed)
	assert.NotContains(t, f.config(t), "preset:npm")
}

func TestSuggestWithoutATerminalOnlyPrints(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "Cargo.toml")
	f.deps.stdinIsTerminal = func() bool { return false }

	// act
	err := f.suggest("y\n")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "Add them to allow with aibox config edit:\n  - preset:cargo\n")
	assert.NotContains(t, f.config(t), "preset:cargo")
}

func TestSuggestPrintsWhatItCannotAdd(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "go.mod")
	f.writeConfig(t, "allow: [api.anthropic.com]\n")

	// act
	err := f.suggest("y\n")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "Add them to allow with aibox config edit:\n  - preset:go\n")
	assert.Equal(t, "allow: [api.anthropic.com]\n", f.config(t))
}

func TestSuggestTakesOnlyPlainFiles(t *testing.T) {
	// arrange
	f := newFixture(t)
	require.NoError(t, os.Mkdir(filepath.Join(f.cwd, "go.mod"), 0o700))
	outside := filepath.Join(t.TempDir(), "package.json")
	require.NoError(t, os.WriteFile(outside, []byte("{}"), 0o600))
	symlink(t, outside, filepath.Join(f.cwd, "package.json"))

	// act
	err := f.suggest("y\n")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "aibox: nothing to suggest")
	assert.Contains(t, f.stderr.String(), "go.mod, go.work, ")
}
