//go:build linux || (darwin && cgo)

package fence_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/fence"
)

// The test binary is also the helper, since the package starts the
// running program as one.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == fence.Name {
		if err := fence.Serve(os.Args[2:], os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		os.Exit(0)
	}

	os.Exit(m.Run())
}

// write runs sh in the helper, which may write below dir, to write to
// the file. The second sh is a program the first starts, which inherits
// the helper's confinement.
func write(dir, file string) ([]byte, error) {
	return fence.Command(dir, "sh", "-c", `sh -c 'echo written > "$1"' sh "$1"`, "sh", file).CombinedOutput()
}

func TestAProgramWritesBelowItsFolder(t *testing.T) {
	// arrange
	dir := t.TempDir()
	file := filepath.Join(dir, "file")

	// act
	out, err := write(dir, file)

	// assert
	require.NoError(t, err, string(out))
	assert.FileExists(t, file)
}

func TestAProgramCannotWriteThroughALinkOutOfItsFolder(t *testing.T) {
	// arrange
	dir, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "target")
	require.NoError(t, os.WriteFile(target, []byte("mine\n"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "link")))

	// act
	out, err := write(dir, filepath.Join(dir, "link"))

	// assert
	require.Error(t, err, string(out))

	data, err := os.ReadFile(target) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Equal(t, "mine\n", string(data))
}

func TestAProgramCannotCreateAFileThroughALinkedFolder(t *testing.T) {
	// arrange
	dir, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "link")))

	// act
	out, err := write(dir, filepath.Join(dir, "link", "file"))

	// assert
	require.Error(t, err, string(out))
	assert.NoFileExists(t, filepath.Join(outside, "file"))
}

func TestAProgramWithoutAFolderWritesNowhereButDevNull(t *testing.T) {
	// arrange
	dir := t.TempDir()

	// act
	nowhere, nowhereErr := write("", filepath.Join(dir, "file"))
	devNull, devNullErr := write("", os.DevNull)

	// assert
	require.Error(t, nowhereErr, string(nowhere))
	assert.NoFileExists(t, filepath.Join(dir, "file"))
	require.NoError(t, devNullErr, string(devNull))
}

func TestAProgramStillReadsOutsideItsFolder(t *testing.T) {
	// arrange
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, []byte("hello\n"), 0o600))

	// act
	out, err := fence.Command("", "cat", file).Output()

	// assert
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(out))
}

func TestWriteFileWritesANewFileBelowTheFolder(t *testing.T) {
	// arrange
	dir := t.TempDir()

	// act
	err := fence.WriteFile(dir, filepath.Join("a", "b", "file"), []byte("data"))

	// assert
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "a", "b", "file")) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Equal(t, "data", string(data))
}

func TestWriteFileReplacesALinkInsteadOfFollowingIt(t *testing.T) {
	// arrange
	dir, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "target")
	require.NoError(t, os.WriteFile(target, []byte("mine\n"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "file")))

	// act
	err := fence.WriteFile(dir, "file", []byte("data"))

	// assert
	require.NoError(t, err)

	data, err := os.ReadFile(target) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Equal(t, "mine\n", string(data))

	info, err := os.Lstat(filepath.Join(dir, "file"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
}

func TestWriteFileCannotWriteThroughALinkedFolder(t *testing.T) {
	// arrange
	dir, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "link")))

	// act
	err := fence.WriteFile(dir, filepath.Join("link", "file"), []byte("data"))

	// assert
	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(outside, "file"))
}

func TestTheHelperRefusesAnUnknownJob(t *testing.T) {
	// act
	err := fence.Serve([]string{"remove", t.TempDir(), "file"}, nil)

	// assert
	require.Error(t, err)
}
