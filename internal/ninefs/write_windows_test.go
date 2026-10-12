package ninefs_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/ninefs"
)

// atRemoveDir is the flag of an unlinkat that removes a folder.
const atRemoveDir = 0x200

func TestAFileTheGuestCreatesAndWritesIsOnTheHost(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	src := walk(t, mount(t, dir), "src")

	// act
	f, _, _, err := src.Create("new.go", p9.ReadWrite, 0o644, 1000, 1000)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	_, err = f.WriteAt([]byte("package new\n"), 0)
	require.NoError(t, err)
	require.NoError(t, f.FSync())

	// assert
	b, err := os.ReadFile(filepath.Join(dir, "src", "new.go")) //nolint:gosec // the test's own folder
	require.NoError(t, err)
	assert.Equal(t, "package new\n", string(b))
}

func TestCreatingANameThatIsThereFails(t *testing.T) {
	t.Parallel()

	// act
	_, _, _, err := walk(t, mount(t, tree(t)), "src").Create("main.go", p9.ReadWrite, 0o644, 1000, 1000)

	// assert
	assert.ErrorIs(t, err, linux.EEXIST)
}

func TestAFolderTheGuestMakesIsOnTheHost(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)

	// act
	_, err := mount(t, dir).Mkdir("build", 0o755, 1000, 1000)

	// assert
	require.NoError(t, err)
	assert.DirExists(t, filepath.Join(dir, "build"))
}

func TestAnUnlinkRemovesTheNameWhileTheGuestHasTheFileOpen(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	src := walk(t, mount(t, dir), "src")

	held := walk(t, src, "main.go")
	_, _, err := held.Open(p9.ReadOnly)
	require.NoError(t, err)

	// act
	err = src.UnlinkAt("main.go", 0)

	// assert: gone by name, and still readable through the open file, as
	// on Linux
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(dir, "src", "main.go"))

	buf := make([]byte, 100)
	n, err := held.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, "package main\n", string(buf[:n]))
}

func TestAnUnlinkIsBusyWhileAProgramOfTheHostHoldsTheFile(t *testing.T) {
	t.Parallel()

	// arrange: Windows programs open files without letting others delete
	// them, Go's os.Open among them
	dir := tree(t)
	held, err := os.Open(filepath.Join(dir, "src", "main.go")) //nolint:gosec // the test's own folder
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })

	// act
	err = walk(t, mount(t, dir), "src").UnlinkAt("main.go", 0)

	// assert
	assert.ErrorIs(t, err, linux.EBUSY)
}

func TestAnUnlinkRemovesWhatItIsToldTheKindOf(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		in    []string
		name  string
		flags uint32
		want  linux.Errno
	}{
		"a folder without the flag": {nil, "src", 0, linux.EISDIR},
		"a file with the flag":      {[]string{"src"}, "main.go", atRemoveDir, linux.ENOTDIR},
		"a folder not empty":        {nil, "src", atRemoveDir, linux.ENOTEMPTY},
		"a name not there":          {nil, "nothing", 0, linux.ENOENT},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			dir := walk(t, mount(t, tree(t)), c.in...)

			// act
			err := dir.UnlinkAt(c.name, c.flags)

			// assert
			assert.ErrorIs(t, err, c.want)
		})
	}
}

func TestARenameReplacesTheFileOfTheNewNameWhileTheGuestHasItOpen(t *testing.T) {
	t.Parallel()

	// arrange: the way git writes a file, to a temporary name it renames
	dir := tree(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.go.lock"), []byte("new"), 0o600))

	src := walk(t, mount(t, dir), "src")

	held := walk(t, src, "main.go")
	_, _, err := held.Open(p9.ReadOnly)
	require.NoError(t, err)

	// act
	err = src.RenameAt("main.go.lock", src, "main.go")

	// assert
	require.NoError(t, err)

	b, err := os.ReadFile(filepath.Join(dir, "src", "main.go")) //nolint:gosec // the test's own folder
	require.NoError(t, err)
	assert.Equal(t, "new", string(b))
	assert.NoFileExists(t, filepath.Join(dir, "src", "main.go.lock"))
}

func TestARenameMovesAFileToAnotherFolder(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	root := mount(t, dir)

	// act
	err := walk(t, root, "src").RenameAt("main.go", root, "main.go")

	// assert
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "main.go"))
	assert.NoFileExists(t, filepath.Join(dir, "src", "main.go"))
}

func TestARenameTakesANameOfOneCharacter(t *testing.T) {
	t.Parallel()

	// arrange: a name of one character makes a buffer shorter than the
	// structure Windows wants
	dir := tree(t)
	src := walk(t, mount(t, dir), "src")

	// act
	err := src.RenameAt("main.go", src, "b")

	// assert
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "src", "b"))
}

func TestSetAttrTruncatesAndSetsTheTimeOfModification(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	f := walk(t, mount(t, dir), "src", "main.go")
	when := time.Date(2020, 1, 2, 3, 4, 5, 600, time.UTC)

	// act
	err := f.SetAttr(p9.SetAttrMask{Size: true, MTime: true, MTimeNotSystemTime: true}, p9.SetAttr{
		Size:             4,
		MTimeSeconds:     uint64(when.Unix()),       //nolint:gosec // a date of 2020
		MTimeNanoSeconds: uint64(when.Nanosecond()), //nolint:gosec // nanoseconds below a second
	})

	// assert
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dir, "src", "main.go"))
	require.NoError(t, err)
	assert.Equal(t, int64(4), info.Size())
	assert.True(t, info.ModTime().Equal(when.Truncate(100*time.Nanosecond)), "%v", info.ModTime())
}

func TestAHardLinkIsTheSameFile(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	root := mount(t, dir)
	f := walk(t, root, "src", "main.go")

	// act
	err := root.Link(f, "linked.go")

	// assert
	require.NoError(t, err)

	a, err := os.Stat(filepath.Join(dir, "src", "main.go"))
	require.NoError(t, err)
	b, err := os.Stat(filepath.Join(dir, "linked.go"))
	require.NoError(t, err)
	assert.True(t, os.SameFile(a, b))
}

func TestAHardLinkTakesANameOfOneCharacter(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	root := mount(t, dir)
	f := walk(t, root, "src", "main.go")

	// act
	err := root.Link(f, "h")

	// assert
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "h"))
}

func TestAReadOnlyShareRefusesEveryChange(t *testing.T) {
	t.Parallel()

	// arrange
	root := mountShare(t, ninefs.Share{Name: "project", Dir: tree(t), ReadOnly: true})
	src := walk(t, root, "src")
	f := walk(t, src, "main.go")

	// act
	_, _, openErr := f.Open(p9.ReadWrite)
	_, _, _, createErr := src.Create("x", p9.WriteOnly, 0o644, 1000, 1000)
	_, mkdirErr := root.Mkdir("x", 0o755, 1000, 1000)
	unlinkErr := src.UnlinkAt("main.go", 0)
	renameErr := src.RenameAt("main.go", src, "x")
	setAttrErr := f.SetAttr(p9.SetAttrMask{Size: true}, p9.SetAttr{})

	// assert
	for _, err := range []error{openErr, createErr, mkdirErr, unlinkErr, renameErr, setAttrErr} {
		assert.ErrorIs(t, err, linux.EROFS)
	}
}

// twoShares serves a writable and a read-only share and returns their
// roots, as one guest would mount them both.
func twoShares(t *testing.T) (writable, readOnly p9.File) {
	t.Helper()

	c := serve(t, ninefs.Share{Name: "project", Dir: tree(t)}, ninefs.Share{Name: "tools", Dir: tree(t), ReadOnly: true})

	return attach(t, c, "project"), attach(t, c, "tools")
}

func TestALinkCannotMakeAFileOfAReadOnlyShareWritable(t *testing.T) {
	t.Parallel()

	// arrange
	writable, readOnly := twoShares(t)
	f := walk(t, readOnly, "src", "main.go")

	// act
	err := writable.Link(f, "sneak")

	// assert
	assert.ErrorIs(t, err, linux.EXDEV)
}

func TestARenameCannotMoveAFileToAnotherShare(t *testing.T) {
	t.Parallel()

	// arrange: two writable shares of one guest
	c := serve(t, ninefs.Share{Name: "project", Dir: tree(t)}, ninefs.Share{Name: "home", Dir: tree(t)})

	project := attach(t, c, "project")
	home := attach(t, c, "home")

	// act
	err := walk(t, project, "src").RenameAt("main.go", home, "main.go")

	// assert
	assert.ErrorIs(t, err, linux.EXDEV)
}

func TestFSyncWorksOnAFileOpenedToReadAndOnAFolder(t *testing.T) {
	t.Parallel()

	// arrange
	root := mount(t, tree(t))
	f := walk(t, root, "src", "main.go")
	dir := walk(t, root, "src")

	_, _, err := f.Open(p9.ReadOnly)
	require.NoError(t, err)
	_, _, err = dir.Open(p9.ReadOnly)
	require.NoError(t, err)

	// act
	fileErr := f.FSync()
	dirErr := dir.FSync()

	// assert
	assert.NoError(t, fileErr)
	assert.NoError(t, dirErr)
}

func TestCreatingANameAFolderHasFails(t *testing.T) {
	t.Parallel()

	// act
	_, _, _, err := mount(t, tree(t)).Create("src", p9.ReadWrite, 0o644, 1000, 1000)

	// assert
	assert.ErrorIs(t, err, linux.EEXIST)
}

func TestReadsAndWritesGoToTheirOffset(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	f := walk(t, mount(t, dir), "src", "main.go")

	_, _, err := f.Open(p9.ReadWrite)
	require.NoError(t, err)

	// act
	_, writeErr := f.WriteAt([]byte("MAIN"), 8)

	buf := make([]byte, 4)
	n, readErr := f.ReadAt(buf, 8)

	past := make([]byte, 4)
	pastN, pastErr := f.ReadAt(past, 1000)

	// assert
	require.NoError(t, writeErr)
	require.NoError(t, readErr)
	assert.Equal(t, "MAIN", string(buf[:n]))
	// the server answers no bytes and no error, which the library's
	// client turns into io.EOF
	assert.ErrorIs(t, pastErr, io.EOF)
	assert.Zero(t, pastN)

	b, err := os.ReadFile(filepath.Join(dir, "src", "main.go")) //nolint:gosec // the test's own folder
	require.NoError(t, err)
	assert.Equal(t, "package MAIN\n", string(b))
}

func TestAnOffsetBeyondInt64IsRefused(t *testing.T) {
	t.Parallel()

	// arrange: Windows would read offset -1 as the end of the file
	f := opened(t, direct(t, tree(t)), "src", "main.go")

	// act
	_, writeErr := f.WriteAt([]byte("x"), -1)
	_, readErr := f.ReadAt(make([]byte, 1), -2)

	// assert
	assert.ErrorIs(t, writeErr, linux.EINVAL)
	assert.ErrorIs(t, readErr, linux.EINVAL)
}

func TestAnUnlinkOrRenameOfNoNameIsRefused(t *testing.T) {
	t.Parallel()

	// arrange: past the library, which refuses an empty name itself, the
	// server refuses it too
	src := walk(t, direct(t, tree(t)), "src")

	// act
	unlinkErr := src.UnlinkAt("", atRemoveDir)
	renameErr := src.RenameAt("", src, "elsewhere")

	// assert
	assert.ErrorIs(t, unlinkErr, linux.EINVAL)
	assert.ErrorIs(t, renameErr, linux.EINVAL)
}
