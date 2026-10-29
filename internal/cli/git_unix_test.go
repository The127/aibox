//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitBytesCleansWhatGitPrintedFromItsError(t *testing.T) {
	// arrange: a git that passes on what the repository, which the VM may
	// have written, says, without replacing control characters itself
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf 'fatal: \\033]52;c;aGk=\\007\\033[2J\\nsecond line\\n' >&2\nexit 128\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755)) //nolint:gosec // the script must run
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// act
	_, err := gitBytes(t.TempDir(), "", "rev-parse", "HEAD")

	// assert
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "\x1b")
	assert.NotContains(t, err.Error(), "\x07")
	assert.NotContains(t, err.Error(), "\n", "a line of git cannot pass for a line of aibox")
	assert.Contains(t, err.Error(), "git rev-parse")
	assert.Contains(t, err.Error(), " | second line")
}
