package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/config"
)

func TestLoadReadsTheGitRemotes(t *testing.T) {
	// arrange
	path := write(t, "git:\n  - remote: GitHub.com/Owner/Repo.git\n    fetch: true\n    push:\n      - aibox/*\n      - feature/login\n  - remote: gitlab.example.com/group/sub/module\n    fetch: true\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.GitRemote{
		{Remote: "github.com/Owner/Repo", Fetch: true, Push: []string{"aibox/*", "feature/login"}},
		{Remote: "gitlab.example.com/group/sub/module", Fetch: true},
	}, cfg.Git)
}

func TestLoadRejectsARemoteThatIsNotHostAndPath(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/owner/repo",
		"git@github.com:owner/repo",
		"github.com",
		"github.com/",
		"localhost/owner/repo",
		"127.0.0.1/owner/repo",
		"github.com:8443/owner/repo",
		"github.com/owner/../repo",
		"github.com/owner//repo",
		"github.com/owner/re po",
		"github.com/owner/repo?x",
		"*.github.com/owner/repo",
		"github.com/-owner/repo",
		"github.com/~owner/repo",
	} {
		t.Run(remote, func(t *testing.T) {
			// arrange
			path := write(t, "git:\n  - remote: \""+remote+"\"\n    fetch: true\n")

			// act
			_, err := config.Load(path)

			// assert
			require.ErrorIs(t, err, config.ErrBadRemote)
		})
	}
}

func TestLoadRejectsAPushPatternThatIsNoBranch(t *testing.T) {
	for _, pattern := range []string{"refs/heads/main", "", "aibox/", "/main", "a..b", ".hidden", "a b", "a:b", "[", "a\\b", "~a"} {
		t.Run(pattern, func(t *testing.T) {
			// arrange
			path := write(t, "git:\n  - remote: github.com/owner/repo\n    push:\n      - \""+pattern+"\"\n")

			// act
			_, err := config.Load(path)

			// assert
			require.ErrorIs(t, err, config.ErrBadBranchPattern)
		})
	}
}

func TestLoadRejectsAGitEntryThatAllowsNothing(t *testing.T) {
	// arrange
	path := write(t, "git:\n  - remote: github.com/owner/repo\n")

	// act
	_, err := config.Load(path)

	// assert
	require.ErrorIs(t, err, config.ErrNothingAllowed)
}

func TestLoadRejectsARemoteNamedTwice(t *testing.T) {
	// arrange
	path := write(t, "git:\n  - remote: github.com/owner/repo\n    fetch: true\n  - remote: GitHub.com/owner/repo.git\n    fetch: true\n")

	// act
	_, err := config.Load(path)

	// assert
	require.ErrorIs(t, err, config.ErrRemoteTwice)
}

func TestLoadWritesAGitExampleIntoTheDefaultFile(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Contains(t, string(content), "# git:\n#   - remote: ")
}
