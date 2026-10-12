package project_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/project"
)

func TestEscape(t *testing.T) {
	// arrange
	tests := map[string]string{
		"/home/someone/repos/aibox":  "-home-someone-repos-aibox",
		"/home/someone/.config/x_y":  "-home-someone-_2econfig-x_5fy",
		"/home/someone/My Project 2": "-home-someone-My_20Project_202",
		"/work/my-app":               "-work-my_2dapp",
		"/work/my/app":               "-work-my-app",
		"/work/my_2dapp":             "-work-my_5f2dapp",
		"/home/someone/Müller":       "-home-someone-M_c3_bcller",
	}

	if runtime.GOOS == "windows" {
		t.Skip("the paths are of Unix")
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

// aProject is the path of a project on this host, and the name of its
// folder.
func aProject() (path, escaped string) {
	if runtime.GOOS == "windows" {
		return `C:\home\someone\repos\aibox`, "C_3a-home-someone-repos-aibox"
	}

	return "/home/someone/repos/aibox", "-home-someone-repos-aibox"
}

func TestOpen(t *testing.T) {
	// arrange
	base := t.TempDir()
	path, escaped := aProject()
	dir := filepath.Join(base, escaped)

	// act
	p, err := project.Open(base, path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, dir, p.Dir)
	assert.Equal(t, filepath.Join(dir, "home"), p.Home)
	assert.Equal(t, filepath.Join(dir, "config.yaml"), p.Config)
	assert.Equal(t, filepath.Join(dir, "proxy.log"), p.Log)
	assert.Equal(t, filepath.Join(dir, "console.log"), p.ConsoleLog)
	assert.Equal(t, filepath.Join(dir, "state.ext4"), p.State)
	assert.Equal(t, filepath.Join(dir, "path"), p.Path)

	// the path goes into its file, since the name of the folder doesn't
	// give it back
	written, err := os.ReadFile(p.Path)
	require.NoError(t, err)
	assert.Equal(t, path+"\n", string(written))

	// Windows has no such modes, the ACL of the home folder keeps it
	if runtime.GOOS != "windows" {
		info, err := os.Stat(p.Home)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}
}

func TestOpenCleansThePath(t *testing.T) {
	// arrange
	base := t.TempDir()
	path, escaped := aProject()
	dir := filepath.Join(base, escaped)
	sep := string(filepath.Separator)

	for _, unclean := range []string{path + sep, filepath.Dir(path) + sep + "other" + sep + ".." + sep + "aibox"} {
		t.Run(unclean, func(t *testing.T) {
			// act
			p, err := project.Open(base, unclean)

			// assert
			require.NoError(t, err)
			assert.Equal(t, dir, p.Dir)
		})
	}
}

func TestOpenKeepsTheHomeFolder(t *testing.T) {
	// arrange
	base := t.TempDir()
	path, _ := aProject()
	p, err := project.Open(base, path)
	require.NoError(t, err)

	login := filepath.Join(p.Home, ".claude.json")
	require.NoError(t, os.WriteFile(login, []byte("{}"), 0o600))

	// act
	_, err = project.Open(base, path)

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
	path, escaped := aProject()

	// act
	_, err := project.Open(base, path)

	// assert
	assert.ErrorContains(t, err, filepath.Join(base, escaped, "home"))
}

func TestAtCreatesNothing(t *testing.T) {
	// arrange
	base := t.TempDir()
	path, escaped := aProject()

	// act
	p, err := project.At(base, path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, project.In(filepath.Join(base, escaped)), p)
	assert.NoDirExists(t, p.Dir)
}

func TestAtRelativePath(t *testing.T) {
	// act
	_, err := project.At(t.TempDir(), "repos/aibox")

	// assert
	assert.ErrorIs(t, err, project.ErrRelativePath)
}

func TestEscapeCutsANameThatIsTooLong(t *testing.T) {
	// arrange
	long := string(filepath.Separator) + strings.Repeat("a.", 100)
	longer := long + "b"

	// act
	name := project.Escape(long)

	// assert
	assert.LessOrEqual(t, len(name), 255)
	assert.Contains(t, name, "_h")
	assert.NotEqual(t, name, project.Escape(longer), "the hash keeps paths with the same start apart")
	assert.Equal(t, name, project.Escape(long))
}
