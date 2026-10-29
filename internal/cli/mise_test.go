package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/project"
)

func writeIn(t *testing.T, dir, name, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

func TestReadMiseFilesReadsTheFilesThereAre(t *testing.T) {
	// arrange
	dir := t.TempDir()
	writeIn(t, dir, ".tool-versions", "node 22\n")

	// act
	files, err := readMiseFiles(dir)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []miseFile{{name: ".tool-versions", content: []byte("node 22\n")}}, files)
}

func TestHashMiseFilesTellsFilesApart(t *testing.T) {
	// arrange
	one := []miseFile{{name: "mise.toml", content: []byte("a")}}
	others := [][]miseFile{
		{{name: "mise.toml", content: []byte("b")}},
		{{name: ".tool-versions", content: []byte("a")}},
		{{name: "mise.toml", content: []byte("a")}, {name: ".tool-versions", content: []byte("")}},
	}

	for _, other := range others {
		// act and assert
		assert.NotEqual(t, hashMiseFiles(one), hashMiseFiles(other))
	}
}

// miseFixture is a project with a mise file and the answer the person gives.
func miseFixture(t *testing.T, answer string) (*fixture, project.Project) {
	t.Helper()

	f := newFixture(t)
	f.deps.stdin = strings.NewReader(answer)
	writeIn(t, f.cwd, "mise.toml", "[tools]\ngo = \"1.25\"\n")

	return f, project.In(t.TempDir())
}

func TestApproveMiseKeepsWhatThePersonSaidYesTo(t *testing.T) {
	// arrange
	f, p := miseFixture(t, "y\n")

	// act
	approved, err := approveMise(context.Background(), f.deps, f.cwd, p)

	// assert
	require.NoError(t, err)
	assert.True(t, approved)
	assert.Contains(t, f.stderr.String(), `  go = "1.25"`)

	again, err := approveMise(context.Background(), f.deps, f.cwd, p)
	require.NoError(t, err)
	assert.True(t, again, "approved files go in without a question")
}

func TestApproveMiseStartsWithoutTheToolsOnANo(t *testing.T) {
	// arrange
	f, p := miseFixture(t, "n\n")

	// act
	approved, err := approveMise(context.Background(), f.deps, f.cwd, p)

	// assert
	require.NoError(t, err)
	assert.False(t, approved)
	assert.NoFileExists(t, p.Mise)
}

func TestApproveMiseAsksAgainWhenAFileChanged(t *testing.T) {
	// arrange
	f, p := miseFixture(t, "y\n")
	_, err := approveMise(context.Background(), f.deps, f.cwd, p)
	require.NoError(t, err)

	writeIn(t, f.cwd, ".tool-versions", "node 22\n")
	f.deps.stdin = strings.NewReader("n\n")

	// act
	approved, err := approveMise(context.Background(), f.deps, f.cwd, p)

	// assert
	require.NoError(t, err)
	assert.False(t, approved)
}

// commitMise commits the files into a new repository and returns it and
// the commit.
func commitMise(t *testing.T, files map[string]string, links map[string]string) (string, string) {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		writeIn(t, dir, name, content)
	}

	for name, target := range links {
		require.NoError(t, os.Symlink(target, filepath.Join(dir, name)))
	}

	var out []byte

	for _, args := range [][]string{
		{"init", "--quiet"},
		{"add", "--all"},
		{"-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--message", "one"},
		{"rev-parse", "HEAD"},
	} {
		cmd := exec.Command("git", args...) //nolint:gosec // the test's own arguments
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")

		var err error

		out, err = cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}

	return dir, strings.TrimSpace(string(out))
}

func TestReadCommitMiseFilesReadsTheFilesAsCommitted(t *testing.T) {
	// arrange
	dir, commit := commitMise(t, map[string]string{"mise.toml": "[tools]\ngo = \"1.25\"\n\n"}, nil)
	writeIn(t, dir, "mise.toml", "changed since\n")

	// act
	files, err := readCommitMiseFiles(dir, commit)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []miseFile{{name: "mise.toml", content: []byte("[tools]\ngo = \"1.25\"\n\n")}}, files)
}

func TestTaskMiseTakesOnlyFilesApprovedBefore(t *testing.T) {
	// arrange
	dir, commit := commitMise(t, map[string]string{"mise.toml": "[tools]\ngo = \"1.25\"\n"}, nil)
	p := project.In(t.TempDir())

	var out strings.Builder

	// act
	before := miseOfCommit(&out, dir, commit, p, "the task")

	files, err := readCommitMiseFiles(dir, commit)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p.Mise, []byte(hashMiseFiles(files)+"\n"), 0o600))

	after := miseOfCommit(&out, dir, commit, p, "the task")

	// assert
	assert.False(t, before)
	assert.True(t, after)
	assert.Contains(t, out.String(), "approve the mise files of the commit in aibox run first")
}

func TestHashMiseFilesTakesACRLFForANewline(t *testing.T) {
	// arrange
	lf := []miseFile{{name: "mise.toml", content: []byte("[tools]\ngo = \"1.25\"\n")}}
	crlf := []miseFile{{name: "mise.toml", content: []byte("[tools]\r\ngo = \"1.25\"\r\n")}}

	// act and assert
	assert.Equal(t, hashMiseFiles(lf), hashMiseFiles(crlf))
}

func TestReadCommitMiseFilesReadsTheTopOfTheRepositoryFromASubfolder(t *testing.T) {
	// arrange
	dir, commit := commitMise(t, map[string]string{"mise.toml": "[tools]\n"}, nil)
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o750))

	// act
	files, err := readCommitMiseFiles(sub, commit)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []miseFile{{name: "mise.toml", content: []byte("[tools]\n")}}, files)
}
