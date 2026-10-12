package hvinstall

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hvserver"
	"github.com/the127/aibox/internal/vhd"
)

// isMe tells an owner that is whoever runs the test, who stands for the
// Administrator an install would run as.
// It asks Windows who owns a folder the test makes: an elevated
// Administrator's folders belong to the Administrators, not to them.
func isMe(t *testing.T) func(*windows.SID) bool {
	t.Helper()

	probe := filepath.Join(t.TempDir(), "probe")
	require.NoError(t, os.Mkdir(probe, 0o750))

	sd, err := windows.GetNamedSecurityInfo(probe, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	require.NoError(t, err)

	mine, _, err := sd.Owner()
	require.NoError(t, err)

	return func(owner *windows.SID) bool { return owner.Equals(mine) }
}

// daclOf is the access list of the folder, as SDDL. It leaves out AI,
// which Windows sets on a list set after the folder was made, and which
// changes nothing of who may do what.
func daclOf(t *testing.T, path string) string {
	t.Helper()

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)

	return strings.Replace(sd.String(), "D:PAI(", "D:P(", 1)
}

func TestMakeFolderMakesAFolderOnlyAdministratorsMayChange(t *testing.T) {
	t.Parallel()

	// arrange
	path := filepath.Join(t.TempDir(), "aibox")

	// act
	err := makeFolder(path, isMe(t))

	// assert
	require.NoError(t, err)
	assert.Equal(t, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FR;;;S-1-5-83-0)", daclOf(t, path))
}

func TestMakeFolderTakesAFolderOfAnAdministratorAndSecuresIt(t *testing.T) {
	t.Parallel()

	// arrange
	path := filepath.Join(t.TempDir(), "aibox")
	require.NoError(t, os.Mkdir(path, 0o750))

	// act
	err := makeFolder(path, isMe(t))

	// assert
	require.NoError(t, err)
	assert.Equal(t, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FR;;;S-1-5-83-0)", daclOf(t, path))
}

func TestMakeFolderRefusesAFolderSomeoneElseMade(t *testing.T) {
	t.Parallel()

	// arrange
	path := filepath.Join(t.TempDir(), "aibox")
	require.NoError(t, os.Mkdir(path, 0o750))

	// act
	err := makeFolder(path, func(*windows.SID) bool { return false })

	// assert
	assert.ErrorIs(t, err, ErrForeignFolder)
}

func TestMakeFolderRefusesALink(t *testing.T) {
	t.Parallel()

	// arrange: a junction, which anyone may make, to a folder of theirs
	dir := t.TempDir()
	target := filepath.Join(dir, "theirs")
	require.NoError(t, os.Mkdir(target, 0o750))

	path := filepath.Join(dir, "aibox")
	out, err := exec.Command("cmd", "/c", "mklink", "/J", path, target).CombinedOutput() //nolint:gosec // the test's own folders
	require.NoError(t, err, "%s", out)

	// act
	err = makeFolder(path, isMe(t))

	// assert
	assert.ErrorIs(t, err, ErrForeignFolder)
}

func TestPlaceImagePutsTheKernelAndTheRootAsAVHDInTheFolder(t *testing.T) {
	t.Parallel()

	// arrange
	image := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(image, imageKernel), []byte("kernel"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(image, imageRoot), make([]byte, 1<<20), 0o600))

	folder := t.TempDir()

	// act
	err := placeImage(image, folder)

	// assert
	require.NoError(t, err)

	kernel, err := os.ReadFile(filepath.Join(folder, hvserver.KernelFile)) //nolint:gosec // a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "kernel", string(kernel))

	root, err := os.Stat(filepath.Join(folder, hvserver.RootFile))
	require.NoError(t, err)
	assert.Equal(t, int64(1<<20+vhd.FooterSize), root.Size())

	left, err := filepath.Glob(filepath.Join(folder, "*.new"))
	require.NoError(t, err)
	assert.Empty(t, left, "no copy is left half made")
}

func TestPlaceImageReplacesTheImageOfAnInstallBefore(t *testing.T) {
	t.Parallel()

	// arrange
	image := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(image, imageKernel), []byte("new kernel"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(image, imageRoot), make([]byte, 1<<20), 0o600))

	folder := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(folder, hvserver.KernelFile), []byte("old kernel"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(folder, hvserver.RootFile), []byte("old root"), 0o600))

	// act
	err := placeImage(image, folder)

	// assert
	require.NoError(t, err)

	kernel, err := os.ReadFile(filepath.Join(folder, hvserver.KernelFile)) //nolint:gosec // a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "new kernel", string(kernel))

	root, err := os.Stat(filepath.Join(folder, hvserver.RootFile))
	require.NoError(t, err)
	assert.Equal(t, int64(1<<20+vhd.FooterSize), root.Size(), "the root of the image, not the old one")
}
