//go:build !windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/project"
)

func TestReadMiseFilesRefusesALink(t *testing.T) {
	// arrange
	dir := t.TempDir()
	writeIn(t, dir, "elsewhere", "[tools]\n")
	require.NoError(t, os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "mise.toml")))

	// act
	_, err := readMiseFiles(dir)

	// assert
	require.ErrorIs(t, err, errMiseLink)
}

func TestApproveMiseStartsWithoutTheToolsWhenAFileIsALink(t *testing.T) {
	// arrange
	f := newFixture(t)
	writeIn(t, f.cwd, "elsewhere", "[tools]\n")
	require.NoError(t, os.Symlink(filepath.Join(f.cwd, "elsewhere"), filepath.Join(f.cwd, "mise.toml")))

	// act
	approved, err := approveMise(context.Background(), f.deps, f.cwd, project.In(t.TempDir()))

	// assert
	require.NoError(t, err)
	assert.False(t, approved)
	assert.Contains(t, f.stderr.String(), "starting without the tools of mise")
}

func TestReadCommitMiseFilesRefusesALink(t *testing.T) {
	// arrange
	dir, commit := commitMise(t, map[string]string{"real": "node 22\n"}, map[string]string{".tool-versions": "real"})

	// act
	_, err := readCommitMiseFiles(dir, commit)

	// assert
	require.ErrorIs(t, err, errMiseLink)
}
