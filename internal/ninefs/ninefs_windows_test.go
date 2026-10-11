package ninefs_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/ninefs"
)

var owner = ninefs.Owner{UID: 1000, GID: 1000}

// mount serves a share of the folder named project and returns its root as
// a 9p client sees it, over the wire like the guest.
func mount(t *testing.T, dir string) p9.File {
	t.Helper()

	return mountShare(t, ninefs.Share{Name: "project", Dir: dir})
}

// mountShare serves the share and returns its root as the guest sees it.
func mountShare(t *testing.T, share ninefs.Share) p9.File {
	t.Helper()

	root, err := serve(t, share).Attach(share.Name)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	return root
}

// serve serves the shares to a 9p client over the wire, as to the guest.
func serve(t *testing.T, shares ...ninefs.Share) *p9.Client {
	t.Helper()

	fs, err := ninefs.New(shares, owner)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fs.Close() })

	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	go func() { _ = p9.NewServer(fs).Handle(server, server) }()

	c, err := p9.NewClient(client)
	require.NoError(t, err)

	return c
}

// tree is a project with a file in a folder.
func tree(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o600))

	return dir
}

// walk walks the names from root and fails the test if it can't.
func walk(t *testing.T, root p9.File, names ...string) p9.File {
	t.Helper()

	_, f, err := root.Walk(names)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	return f
}

func TestAFileOfAShareReadsAsOnTheHost(t *testing.T) {
	t.Parallel()

	// arrange
	f := walk(t, mount(t, tree(t)), "src", "main.go")

	// act
	_, _, err := f.Open(p9.ReadOnly)
	require.NoError(t, err)

	buf := make([]byte, 100)
	n, err := f.ReadAt(buf, 0)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "package main\n", string(buf[:n]))
}

func TestAFolderOfAShareListsItsEntries(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), nil, 0o600))

	root := mount(t, dir)

	// act
	_, _, err := root.Open(p9.ReadOnly)
	require.NoError(t, err)

	entries, err := root.Readdir(0, 64<<10)

	// assert
	require.NoError(t, err)

	names := map[string]p9.QIDType{}
	for _, e := range entries {
		names[e.Name] = e.Type
	}

	assert.Equal(t, map[string]p9.QIDType{"src": p9.TypeDir, "README.md": p9.TypeRegular}, names)
}

func TestAListingGoesOnFromTheOffsetOfItsLastEntry(t *testing.T) {
	t.Parallel()

	// arrange
	dir := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}

	root := mount(t, dir)
	_, _, err := root.Open(p9.ReadOnly)
	require.NoError(t, err)

	// act: a budget for one entry at a time
	var names []string

	for offset := uint64(0); ; {
		entries, err := root.Readdir(offset, 30)
		require.NoError(t, err)

		if len(entries) == 0 {
			break
		}

		names = append(names, entries[0].Name)
		offset = entries[0].Offset
	}

	// assert
	assert.ElementsMatch(t, []string{"a", "b", "c"}, names)
}

func TestEveryFileBelongsToTheOwner(t *testing.T) {
	t.Parallel()

	// arrange
	f := walk(t, mount(t, tree(t)), "src", "main.go")

	// act
	_, _, attr, err := f.GetAttr(p9.AttrMaskAll)

	// assert
	require.NoError(t, err)
	assert.Equal(t, p9.UID(1000), attr.UID)
	assert.Equal(t, p9.GID(1000), attr.GID)
	assert.Equal(t, p9.ModeRegular|0o644, attr.Mode)
	assert.Equal(t, uint64(len("package main\n")), attr.Size)
	assert.NotZero(t, attr.MTimeSeconds)
}

func TestAWalkCannotLeaveTheShare(t *testing.T) {
	t.Parallel()

	for name, names := range map[string][]string{
		"up":                  {".."},
		"up from a folder":    {"src", "..", ".."},
		"through a backslash": {`src\..\..`},
		"to a data stream":    {"src", "main.go:secret"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			_, _, err := mount(t, tree(t)).Walk(names)

			// assert
			assert.ErrorIs(t, err, linux.EINVAL)
		})
	}
}

func TestAWalkDoesNotFollowAJunctionOutOfTheShare(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600))

	out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(dir, "out"), outside).CombinedOutput() //nolint:gosec // the paths are the test's own
	require.NoError(t, err, string(out))

	root := mount(t, dir)

	// act
	_, _, err = root.Walk([]string{"out", "secret"})
	_, _, attr, attrErr := walk(t, root, "out").GetAttr(p9.AttrMaskAll)

	// assert: the library refuses a walk through what is no folder before
	// the share's own check
	require.ErrorIs(t, err, linux.EINVAL)
	require.NoError(t, attrErr)
	assert.Equal(t, p9.ModeSymlink, attr.Mode.FileType())
}

func TestAnUnknownShareIsNotThere(t *testing.T) {
	t.Parallel()

	// arrange
	c := serve(t, ninefs.Share{Name: "project", Dir: t.TempDir()})

	// act
	_, err := c.Attach("home")

	// assert
	assert.ErrorIs(t, err, linux.ENOENT)
}

func TestNewRefusesTwoSharesOfOneName(t *testing.T) {
	t.Parallel()

	// act
	_, err := ninefs.New([]ninefs.Share{{Name: "project", Dir: t.TempDir()}, {Name: "project", Dir: t.TempDir()}}, owner)

	// assert
	assert.ErrorIs(t, err, ninefs.ErrShareTwice)
}
