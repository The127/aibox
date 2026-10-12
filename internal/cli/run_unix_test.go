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

func TestRunTellsNothingWhenTheVMLeftAFIFOForItsSessions(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		require.NoError(t, os.MkdirAll(filepath.Join(spec.Home, ".claude"), 0o700))

		return syscall.Mkfifo(filepath.Join(spec.Home, ".claude", "projects"), 0o600)
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.NotContains(t, f.stderr.String(), "--resume")
}
