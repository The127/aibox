package ninefs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/ninefs"
)

// guestMode is the mode the guest sees of the file.
func guestMode(t *testing.T, f p9.File) p9.FileMode {
	t.Helper()

	_, _, attr, err := f.GetAttr(p9.AttrMaskAll)
	require.NoError(t, err)

	return attr.Mode
}

func TestAFileKeepsTheModeTheGuestCreatesItWith(t *testing.T) {
	t.Parallel()

	// arrange: a create turns the fid of the folder into the new file, so
	// it creates from a copy
	root := mount(t, tree(t))

	f, _, _, err := walk(t, root).Create("run.sh", p9.WriteOnly, 0o755, 1000, 1000)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	// act
	mode := guestMode(t, walk(t, root, "run.sh"))

	// assert
	assert.Equal(t, p9.ModeRegular|0o755, mode)
}

func TestAFolderKeepsTheModeTheGuestMakesItWith(t *testing.T) {
	t.Parallel()

	// arrange
	root := mount(t, tree(t))

	_, err := root.Mkdir("private", 0o700, 1000, 1000)
	require.NoError(t, err)

	// act
	mode := guestMode(t, walk(t, root, "private"))

	// assert
	assert.Equal(t, p9.ModeDirectory|0o700, mode)
}

func TestAChmodStaysAfterTheServerIsGone(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	f := walk(t, mount(t, dir), "src", "main.go")

	require.NoError(t, f.SetAttr(p9.SetAttrMask{Permissions: true}, p9.SetAttr{Permissions: 0o1750}))

	// act: a server of its own, as for the next run of the VM
	mode := guestMode(t, walk(t, mount(t, dir), "src", "main.go"))

	// assert
	assert.Equal(t, p9.ModeRegular|0o1750, mode)
}

func TestAFileTheGuestNeverSetAModeOnHasTheDefault(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	locked := filepath.Join(dir, "locked.txt")
	require.NoError(t, os.WriteFile(locked, nil, 0o600))

	p, err := windows.UTF16PtrFromString(locked)
	require.NoError(t, err)
	require.NoError(t, windows.SetFileAttributes(p, windows.FILE_ATTRIBUTE_READONLY))
	t.Cleanup(func() { _ = windows.SetFileAttributes(p, windows.FILE_ATTRIBUTE_NORMAL) })

	root := mount(t, dir)

	// act
	file := guestMode(t, walk(t, root, "src", "main.go"))
	folder := guestMode(t, walk(t, root, "src"))
	readOnly := guestMode(t, walk(t, root, "locked.txt"))

	// assert
	assert.Equal(t, p9.ModeRegular|0o644, file)
	assert.Equal(t, p9.ModeDirectory|0o755, folder)
	assert.Equal(t, p9.ModeRegular|0o444, readOnly)
}

func TestAChmodOnAReadOnlyShareIsRefused(t *testing.T) {
	t.Parallel()

	// arrange
	f := walk(t, mountShare(t, ninefs.Share{Name: "project", Dir: tree(t), ReadOnly: true}), "src", "main.go")

	// act
	err := f.SetAttr(p9.SetAttrMask{Permissions: true}, p9.SetAttr{Permissions: 0o755})

	// assert
	assert.ErrorIs(t, err, linux.EROFS)
}

func TestASetAttrThatGivesTheFileAnotherOwnerChangesNothing(t *testing.T) {
	t.Parallel()

	// arrange
	f := walk(t, mount(t, tree(t)), "src", "main.go")

	// act
	err := f.SetAttr(p9.SetAttrMask{Permissions: true, UID: true}, p9.SetAttr{Permissions: 0o700, UID: 0})

	// assert
	require.ErrorIs(t, err, linux.EPERM)
	assert.Equal(t, p9.ModeRegular|0o644, guestMode(t, f))
}
