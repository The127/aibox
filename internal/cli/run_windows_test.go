package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/testlink"
)

// A junction is followed as a symlink is, though filepath.EvalSymlinks
// leaves it: one to the root of a drive mounts the drive.
func TestRunRefusesAMountOfAJunctionToTheRootOfADrive(t *testing.T) {
	// arrange
	f := newFixture(t)
	junction := filepath.Join(t.TempDir(), "drive")
	testlink.Junction(t, rootOf(junction), junction)

	f.writeConfig(t, "mounts:\n  - '"+junction+":/mnt/host'\n")

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errUnsafeMount)
	assert.ErrorContains(t, err, "root of the file system")
}

// The check for the root of a file system holds for every drive and
// share, not just the one aibox runs on, and for one that isn't there.
func TestRunRefusesAMountOfTheRootOfAnyDrive(t *testing.T) {
	for _, root := range []string{`Q:\`, `Q:/`, `\\server\share\`} {
		t.Run(root, func(t *testing.T) {
			// arrange
			f := newFixture(t)
			f.writeConfig(t, "mounts:\n  - '"+root+":/mnt/host'\n")

			// act
			err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

			// assert
			require.ErrorIs(t, err, errUnsafeMount)
			assert.ErrorContains(t, err, "root of the file system")
		})
	}
}
