//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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

func TestReadCommitMiseFilesRefusesALink(t *testing.T) {
	// arrange
	dir, commit := commitMise(t, map[string]string{"real": "node 22\n"}, map[string]string{".tool-versions": "real"})

	// act
	_, err := readCommitMiseFiles(dir, commit)

	// assert
	require.ErrorIs(t, err, errMiseLink)
}
