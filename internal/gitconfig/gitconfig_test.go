package gitconfig_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/gitconfig"
)

// git runs git with the arguments of the test.
func git(args ...string) error {
	return exec.Command("git", args...).Run() //nolint:gosec // the arguments are the test's own
}

func set(t *testing.T, args ...string) {
	t.Helper()

	require.NoError(t, git(append([]string{"config"}, args...)...))
}

func TestReadFollowsWhatGitUsesInTheFolder(t *testing.T) {
	// arrange
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	set(t, "--global", "user.name", "Global Name")
	set(t, "--global", "user.email", "global@example.com")

	repo := t.TempDir()
	err := git("-C", repo, "init", "-q")
	require.NoError(t, err)

	err = git("-C", repo, "config", "user.email", "repo@example.com")
	require.NoError(t, err)

	// act
	identity := gitconfig.Read(repo, exec.Command)

	// assert
	assert.Equal(t, gitconfig.Identity{Name: "Global Name", Email: "repo@example.com"}, identity)
}

func TestReadIsEmptyWithoutAnIdentity(t *testing.T) {
	// arrange
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// act
	identity := gitconfig.Read(t.TempDir(), exec.Command)

	// assert
	assert.Equal(t, gitconfig.Identity{}, identity)
}

func TestAutoCRLFFollowsWhatGitUsesInTheFolder(t *testing.T) {
	// arrange: the config of the system, as Git for Windows writes it, and
	// a repository that says otherwise
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	system := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", system)
	set(t, "--file", system, "core.autocrlf", "true")

	plain := t.TempDir()
	repo := t.TempDir()
	require.NoError(t, git("-C", repo, "init", "-q"))
	require.NoError(t, git("-C", repo, "config", "core.autocrlf", "input"))

	// act
	fromSystem := gitconfig.AutoCRLF(plain, exec.Command)
	fromRepo := gitconfig.AutoCRLF(repo, exec.Command)

	// assert
	assert.Equal(t, "true", fromSystem)
	assert.Equal(t, "input", fromRepo)
}

func TestAutoCRLFIsEmptyWhenGitHasNone(t *testing.T) {
	// arrange
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// act
	value := gitconfig.AutoCRLF(t.TempDir(), exec.Command)

	// assert
	assert.Empty(t, value)
}
