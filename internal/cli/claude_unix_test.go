//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A FIFO the VM left in place of its settings must not keep aibox waiting
// for a writer that never comes.
func TestRunWaitsForNoFIFOTheVMLeftForTheSettings(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.claudeFile(t, "settings.json", `{"model": "opus"}`)

	vmSettings := filepath.Join(f.project(t).Home, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(vmSettings), 0o700))
	require.NoError(t, syscall.Mkfifo(vmSettings, 0o600))

	// act
	err := f.runWithImage(t)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "the VM gets none of your settings")
}
