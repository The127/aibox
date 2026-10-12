package testlink

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// Folder makes a link at link that leads to the folder target: a symlink,
// or a junction where a symlink takes a privilege this user lacks, which
// a junction does not.
func Folder(t *testing.T, target, link string) {
	t.Helper()

	err := os.Symlink(target, link)
	if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		Junction(t, target, link)

		return
	}

	require.NoError(t, err)
}

// Junction makes a junction at link that leads to the folder target, which
// takes no privilege and can lead to the root of a drive.
func Junction(t *testing.T, target, link string) {
	t.Helper()

	out, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput() //nolint:gosec // the paths are the test's own
	require.NoError(t, err, string(out))
}
