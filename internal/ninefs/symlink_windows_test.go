package ninefs_test

import (
	"net"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/ninefs"
)

func TestALinkTheGuestMakesReadsBackAsItsTarget(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"main.go", "../README.md", "/usr/bin/env", "does/not/exist"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			// arrange
			src := walk(t, mount(t, tree(t)), "src")

			_, err := src.Symlink(target, "link", 1000, 1000)
			require.NoError(t, err)

			link := walk(t, src, "link")

			// act
			got, err := link.Readlink()

			// assert
			require.NoError(t, err)
			assert.Equal(t, target, got)
			assert.Equal(t, p9.ModeSymlink, modeOf(t, link).FileType())
		})
	}
}

func TestAWalkDoesNotFollowALinkTheGuestMade(t *testing.T) {
	t.Parallel()

	// arrange
	root := mount(t, tree(t))

	_, err := root.Symlink("src", "to-src", 1000, 1000)
	require.NoError(t, err)

	// act
	_, _, err = root.Walk([]string{"to-src", "main.go"})

	// assert: the guest resolves links itself; the server never does
	assert.ErrorIs(t, err, linux.EINVAL)
}

func TestAnUnlinkOfALinkLeavesItsTarget(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)
	src := walk(t, mount(t, dir), "src")

	_, err := src.Symlink("main.go", "link", 1000, 1000)
	require.NoError(t, err)

	// act
	err = src.UnlinkAt("link", 0)

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(dir, "src", "link"))
	assert.FileExists(t, filepath.Join(dir, "src", "main.go"))
}

func TestAJunctionReadsBackWithTheSeparatorsOfTheGuest(t *testing.T) {
	t.Parallel()

	// arrange
	dir := tree(t)

	out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(dir, "out"), filepath.Join(dir, "src")).CombinedOutput() //nolint:gosec // the paths are the test's own
	require.NoError(t, err, string(out))

	// act
	got, err := walk(t, mount(t, dir), "out").Readlink()

	// assert
	require.NoError(t, err)
	assert.Equal(t, filepath.ToSlash(filepath.Join(dir, "src")), got)
}

func TestAReparsePointThatIsNoLinkIsAFile(t *testing.T) {
	t.Parallel()

	// arrange: a Unix socket of Windows is a reparse point whose tag
	// stands for no other name, as OneDrive's are
	dir := tree(t)

	l, err := net.Listen("unix", filepath.Join(dir, "sock"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	// act
	mode := modeOf(t, walk(t, mount(t, dir), "sock"))

	// assert
	assert.Equal(t, p9.ModeRegular, mode.FileType())
}

func TestAReadOnlyShareRefusesALink(t *testing.T) {
	t.Parallel()

	// arrange
	root := mountShare(t, ninefs.Share{Name: "project", Dir: tree(t), ReadOnly: true})

	// act
	_, err := root.Symlink("src", "link", 1000, 1000)

	// assert
	assert.ErrorIs(t, err, linux.EROFS)
}

func TestReadlinkOfAFileThatIsNoLinkFails(t *testing.T) {
	t.Parallel()

	// act
	_, err := walk(t, mount(t, tree(t)), "src", "main.go").Readlink()

	// assert
	assert.ErrorIs(t, err, linux.EINVAL)
}
