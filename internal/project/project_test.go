package project_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/project"
)

func TestEscape(t *testing.T) {
	// arrange
	tests := map[string]string{
		"/home/someone/repos/aibox":  "-home-someone-repos-aibox",
		"/home/someone/.config/x_y":  "-home-someone--config-x-y",
		"/home/someone/My Project 2": "-home-someone-My-Project-2",
	}

	for path, want := range tests {
		t.Run(path, func(t *testing.T) {
			// act
			escaped := project.Escape(path)

			// assert
			assert.Equal(t, want, escaped)
		})
	}
}

func TestOpen(t *testing.T) {
	// arrange
	base := t.TempDir()
	dir := filepath.Join(base, "-home-someone-repos-aibox")

	// act
	p, err := project.Open(base, "/home/someone/repos/aibox")

	// assert
	require.NoError(t, err)
	assert.Equal(t, dir, p.Dir)
	assert.Equal(t, filepath.Join(dir, "home"), p.Home)
	assert.Equal(t, filepath.Join(dir, "config.yaml"), p.Config)
	assert.Equal(t, filepath.Join(dir, "proxy.log"), p.Log)
	assert.Equal(t, filepath.Join(dir, "console.log"), p.ConsoleLog)
	assert.Equal(t, filepath.Join(dir, "state.ext4"), p.State)

	info, err := os.Stat(p.Home)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestOpenCleansThePath(t *testing.T) {
	// arrange
	base := t.TempDir()
	dir := filepath.Join(base, "-home-someone-repos-aibox")

	for _, path := range []string{"/home/someone/repos/aibox/", "/home/someone/repos/other/../aibox"} {
		t.Run(path, func(t *testing.T) {
			// act
			p, err := project.Open(base, path)

			// assert
			require.NoError(t, err)
			assert.Equal(t, dir, p.Dir)
		})
	}
}

func TestOpenKeepsTheHomeFolder(t *testing.T) {
	// arrange
	base := t.TempDir()
	p, err := project.Open(base, "/home/someone/repos/aibox")
	require.NoError(t, err)

	login := filepath.Join(p.Home, ".claude.json")
	require.NoError(t, os.WriteFile(login, []byte("{}"), 0o600))

	// act
	_, err = project.Open(base, "/home/someone/repos/aibox")

	// assert
	require.NoError(t, err)
	assert.FileExists(t, login)
}

func TestOpenRelativePath(t *testing.T) {
	// arrange
	base := t.TempDir()

	// act
	_, err := project.Open(base, "repos/aibox")

	// assert
	assert.ErrorIs(t, err, project.ErrRelativePath)
}

func TestOpenWhenBaseIsAFile(t *testing.T) {
	// arrange
	base := filepath.Join(t.TempDir(), "projects")
	require.NoError(t, os.WriteFile(base, nil, 0o600))

	// act
	_, err := project.Open(base, "/home/someone/repos/aibox")

	// assert
	assert.ErrorContains(t, err, filepath.Join(base, "-home-someone-repos-aibox", "home"))
}
