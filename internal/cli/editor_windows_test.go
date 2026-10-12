package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunEditorGivesCmdTheEditorAndThePath(t *testing.T) {
	// arrange: a path with a space, which takes the quotes
	path := filepath.Join(t.TempDir(), "my project", "config.yaml")
	require.NoError(t, os.Mkdir(filepath.Dir(path), 0o700))

	// act
	err := runEditor("echo edited>", path)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "edited\r\n", string(content))
}

// An editor in a folder with a space is set with quotes around its path,
// which cmd keeps once /s took the outer ones.
func TestRunEditorRunsAnEditorWhosePathIsInQuotes(t *testing.T) {
	// arrange
	dir := filepath.Join(t.TempDir(), "my editor")
	require.NoError(t, os.Mkdir(dir, 0o700))

	editor := filepath.Join(dir, "edit.cmd")
	require.NoError(t, os.WriteFile(editor, []byte("@echo edited> %1\r\n"), 0o600))

	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	err := runEditor(`"`+editor+`"`, path)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "edited\r\n", string(content))
}
