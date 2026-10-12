//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The VM can take the rights to a folder of its home away, which the
// files it writes keep on the host. That must not keep a reset or a remove
// from clearing it.
func TestResetAndRemoveClearFoldersTheVMMadeUnwritable(t *testing.T) {
	for _, args := range [][]string{{"reset", "--logout"}, {"remove"}} {
		t.Run(args[0], func(t *testing.T) {
			// arrange
			f := newProjectsFixture(t)
			locked := filepath.Join(f.p.Home, "x", "y")
			f.write(t, filepath.Join(locked, "f"))
			require.NoError(t, os.Chmod(locked, 0))
			require.NoError(t, os.Chmod(filepath.Join(f.p.Home, ".claude"), 0o500)) //nolint:gosec // the VM takes the rights away
			require.NoError(t, os.Chmod(f.p.Home, 0o500))                           //nolint:gosec // the VM takes the rights away
			t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })                       //nolint:gosec // so that the test can remove its folder

			// act
			err := f.projects("y\n", args...)

			// assert
			require.NoError(t, err)
			assert.NoDirExists(t, filepath.Join(f.p.Home, "x"))
			assert.NoDirExists(t, filepath.Join(f.p.Home, ".claude"))
		})
	}
}
