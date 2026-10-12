package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetwdSpellsTheFolderAsTheFileSystemDoes(t *testing.T) {
	// arrange
	dir := t.TempDir()
	want, err := getwdIn(t, dir)
	require.NoError(t, err)

	// act
	got, err := getwdIn(t, strings.ToLower(dir))

	// assert
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// getwdIn is getwd in the folder, spelled as it is given.
func getwdIn(t *testing.T, dir string) (string, error) {
	t.Chdir(dir)

	return getwd()
}
