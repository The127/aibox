package project_test

import (
	"os"
	"path/filepath"
	"syscall"
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

func TestCreateStateMakesASparseFileOfTheSize(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "state.ext4")

	// act
	err := project.CreateState(path, 1<<30)

	// assert
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, int64(1<<30), info.Size())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	var stat syscall.Stat_t
	require.NoError(t, syscall.Stat(path, &stat))
	assert.Less(t, stat.Blocks*512, int64(1<<20), "the file takes up space before anything was written")
}

func TestCreateStateSizesAnEmptyFile(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "state.ext4")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	// act
	err := project.CreateState(path, 1<<30)

	// assert
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, int64(1<<30), info.Size())
}

func TestCreateStateKeepsAnExistingDisk(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "state.ext4")
	require.NoError(t, os.WriteFile(path, []byte("data of the project"), 0o600))

	// act
	err := project.CreateState(path, 1<<30)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "data of the project", string(content))
}
