package gitconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/gitconfig"
)

// helper makes git on the host use a credential helper that answers with
// what the script prints, and nothing else.
func helper(t *testing.T, script string) {
	t.Helper()

	config := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(config, []byte("[credential]\n\thelper = \"!f() { "+script+"; }; f\"\n"), 0o600))

	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func TestLoginForAsksTheCredentialHelperOfTheHost(t *testing.T) {
	// arrange
	asked := filepath.Join(t.TempDir(), "asked")
	helper(t, "cat > "+asked+"; echo username=someone; echo password=the-token")

	// act
	login, err := gitconfig.LoginFor(context.Background(), "github.com/owner/repo")

	// assert
	require.NoError(t, err)
	assert.Equal(t, gitconfig.Login{Username: "someone", Password: "the-token"}, login)

	request, err := os.ReadFile(asked) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Contains(t, string(request), "protocol=https\nhost=github.com\n")
}

func TestLoginForFailsWithoutALogin(t *testing.T) {
	// arrange
	helper(t, "true")

	// act
	_, err := gitconfig.LoginFor(context.Background(), "github.com/owner/repo")

	// assert
	require.ErrorIs(t, err, gitconfig.ErrNoLogin)
	assert.ErrorContains(t, err, "https://github.com/owner/repo")
}

func TestLoginForIgnoresTheRepositoryOfTheCurrentFolder(t *testing.T) {
	// arrange
	helper(t, "true")

	project := t.TempDir()
	_, err := git("init", project)
	require.NoError(t, err)
	_, err = git("-C", project, "config", "credential.helper", "!f() { echo username=vm; echo password=from-the-project; }; f")
	require.NoError(t, err)
	t.Chdir(project)
	t.Setenv("GIT_DIR", filepath.Join(project, ".git"))

	// act
	_, err = gitconfig.LoginFor(context.Background(), "github.com/owner/repo")

	// assert
	require.ErrorIs(t, err, gitconfig.ErrNoLogin)
}

func TestLoginForKeepsTheVariablesAfterThoseOfARepository(t *testing.T) {
	// arrange: the variable of the repository comes first, the config of
	// the helper after it
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), ".git"))
	helper(t, "echo username=someone; echo password=the-token")

	// act
	login, err := gitconfig.LoginFor(context.Background(), "github.com/owner/repo")

	// assert
	require.NoError(t, err)
	assert.Equal(t, gitconfig.Login{Username: "someone", Password: "the-token"}, login)
}
