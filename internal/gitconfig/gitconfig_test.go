package gitconfig_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/gitconfig"
)

// git runs git with the arguments of the test.
func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output() //nolint:gosec // the arguments are the test's own

	return strings.TrimSpace(string(out)), err
}

// get asks git itself for a key in the config file aibox writes.
func get(t *testing.T, home, key string) string {
	t.Helper()

	out, err := git("config", "-f", filepath.Join(home, ".config", "git", "config"), "--get", key)
	if err != nil {
		return ""
	}

	return out
}

func set(t *testing.T, args ...string) {
	t.Helper()

	_, err := git(append([]string{"config"}, args...)...)
	require.NoError(t, err)
}

func TestWriteSetsTheIdentity(t *testing.T) {
	// arrange
	home := t.TempDir()

	// act
	err := gitconfig.Write(home, gitconfig.Identity{Name: `Some "Quoted" One\`, Email: "someone@example.com"})

	// assert
	require.NoError(t, err)
	assert.Equal(t, `Some "Quoted" One\`, get(t, home, "user.name"))
	assert.Equal(t, "someone@example.com", get(t, home, "user.email"))
}

func TestWriteKeepsOtherSettings(t *testing.T) {
	// arrange
	home := t.TempDir()
	require.NoError(t, gitconfig.Write(home, gitconfig.Identity{Name: "Old", Email: "old@example.com"}))
	set(t, "-f", filepath.Join(home, ".config", "git", "config"), "alias.st", "status")

	// act
	err := gitconfig.Write(home, gitconfig.Identity{Name: "New", Email: "new@example.com"})

	// assert
	require.NoError(t, err)
	assert.Equal(t, "New", get(t, home, "user.name"))
	assert.Equal(t, "new@example.com", get(t, home, "user.email"))
	assert.Equal(t, "status", get(t, home, "alias.st"))
}

func TestWriteRemovesAKeyWithoutAValue(t *testing.T) {
	// arrange
	home := t.TempDir()
	require.NoError(t, gitconfig.Write(home, gitconfig.Identity{Name: "Some One", Email: "someone@example.com"}))

	// act
	err := gitconfig.Write(home, gitconfig.Identity{Name: "Some One"})

	// assert
	require.NoError(t, err)
	assert.Equal(t, "Some One", get(t, home, "user.name"))
	assert.Empty(t, get(t, home, "user.email"))
}

func TestReadFollowsWhatGitUsesInTheFolder(t *testing.T) {
	// arrange
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	set(t, "--global", "user.name", "Global Name")
	set(t, "--global", "user.email", "global@example.com")

	repo := t.TempDir()
	_, err := git("-C", repo, "init", "-q")
	require.NoError(t, err)

	_, err = git("-C", repo, "config", "user.email", "repo@example.com")
	require.NoError(t, err)

	// act
	identity := gitconfig.Read(repo)

	// assert
	assert.Equal(t, gitconfig.Identity{Name: "Global Name", Email: "repo@example.com"}, identity)
}

func TestReadIsEmptyWithoutAnIdentity(t *testing.T) {
	// arrange
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// act
	identity := gitconfig.Read(t.TempDir())

	// assert
	assert.Equal(t, gitconfig.Identity{}, identity)
}
