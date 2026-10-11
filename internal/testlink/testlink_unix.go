//go:build !windows

package testlink

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Folder makes a symlink at link that leads to the folder target.
func Folder(t *testing.T, target, link string) {
	t.Helper()

	require.NoError(t, os.Symlink(target, link))
}
