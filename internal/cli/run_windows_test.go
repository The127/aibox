package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
