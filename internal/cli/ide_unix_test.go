//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunWritesNoFileOfTheHostThroughASymlinkTheVMPlanted(t *testing.T) {
	tests := map[string]func(t *testing.T, home, outside string){
		"the lock file": func(t *testing.T, home, outside string) {
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "ide"), 0o700))
			require.NoError(t, os.Symlink(filepath.Join(outside, ".zshrc"), filepath.Join(home, ".claude", "ide", "3131.lock")))
		},
		"the folder of the lock file": func(t *testing.T, home, outside string) {
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
			require.NoError(t, os.Symlink(outside, filepath.Join(home, ".claude", "ide")))
		},
	}

	for name, plant := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newFixture(t)
			image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
			writeHostLock(t, filepath.Join(f.homeDir, ".claude", "ide"), f.cwd)
			outside := t.TempDir()
			zshrc := filepath.Join(outside, ".zshrc")
			require.NoError(t, os.WriteFile(zshrc, []byte("export PATH\n"), 0o600))
			plant(t, f.project(t).Home, outside)

			// act
			_ = f.run("--image", image)

			// assert
			data, err := os.ReadFile(zshrc) //nolint:gosec // a file of the test
			require.NoError(t, err)
			assert.Equal(t, "export PATH\n", string(data))
			assert.NoFileExists(t, filepath.Join(outside, "3131.lock"))
		})
	}
}
