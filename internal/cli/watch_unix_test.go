//go:build !windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
)

func TestRunTellsOfALinkAndAFIFOTheVMPutInPlaceOfWatchedFiles(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	writeFile(t, filepath.Join(f.cwd, ".envrc"), "use flake\n")
	writeFile(t, filepath.Join(f.cwd, "package.json"), "{}\n")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		envrc := filepath.Join(spec.Project, ".envrc")
		require.NoError(t, os.Remove(envrc))
		require.NoError(t, os.Symlink("/etc/passwd", envrc))

		pkg := filepath.Join(spec.Project, "package.json")
		require.NoError(t, os.Remove(pkg))
		require.NoError(t, syscall.Mkfifo(pkg, 0o600))

		return nil
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "  changed  .envrc\n  changed  package.json\n")
}
