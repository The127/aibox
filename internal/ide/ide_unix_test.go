//go:build !windows

package ide_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/ide"
)

func TestFindFollowsSymlinksOnBothSides(t *testing.T) {
	// arrange
	root := t.TempDir()
	target := folder(t, root, "real", "aibox")
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(filepath.Join(root, "real"), link))

	tests := map[string]struct{ ideFolder, project string }{
		"the IDE opened the link":          {filepath.Join(link, "aibox"), target},
		"aibox runs in the link":           {target, filepath.Join(link, "aibox")},
		"the IDE opened the linked parent": {link, target},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeLock(t, dir, "1001.lock", name, []string{test.ideFolder}, time.Now())

			// act
			ides, err := ide.Find(dir, test.project)

			// assert
			require.NoError(t, err)
			require.Len(t, ides, 1)
			assert.Equal(t, name, ides[0].Name)
		})
	}
}

func TestFindSkipsALockFileThatIsASymlink(t *testing.T) {
	// arrange
	dir, root := t.TempDir(), t.TempDir()
	project := folder(t, root, "aibox")
	writeLock(t, root, "elsewhere.json", "a symlink", []string{project}, time.Now())
	require.NoError(t, os.Symlink(filepath.Join(root, "elsewhere.json"), filepath.Join(dir, "1001.lock")))

	// act
	ides, err := ide.Find(dir, project)

	// assert
	require.NoError(t, err)
	assert.Empty(t, ides)
}
