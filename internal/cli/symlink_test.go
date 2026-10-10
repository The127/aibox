package cli

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// symlink makes a link for a test, and skips the test where Windows lets
// only an administrator or a developer make one.
func symlink(t *testing.T, target, link string) {
	t.Helper()

	err := os.Symlink(target, link)
	if errors.Is(err, errSymlinkPrivilege) {
		t.Skip("making a link needs a privilege this user lacks")
	}

	require.NoError(t, err)
}
