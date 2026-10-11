package realpath_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/realpath"
	"github.com/the127/aibox/internal/testlink"
)

func TestOfIsTheFolderItselfWithoutALink(t *testing.T) {
	// arrange
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	// act
	got, err := realpath.Of(dir)

	// assert
	require.NoError(t, err)
	assert.Equal(t, dir, got)
}

func TestOfFollowsALinkToAFolder(t *testing.T) {
	// arrange
	target, err := realpath.Of(t.TempDir())
	require.NoError(t, err)

	via := filepath.Join(t.TempDir(), "via")
	testlink.Folder(t, target, via)

	// act
	got, err := realpath.Of(via)

	// assert
	require.NoError(t, err)
	assert.Equal(t, target, got)
}

func TestOfFailsForWhatIsNotThere(t *testing.T) {
	// act
	_, err := realpath.Of(filepath.Join(t.TempDir(), "none"))

	// assert
	assert.ErrorIs(t, err, os.ErrNotExist)
}
