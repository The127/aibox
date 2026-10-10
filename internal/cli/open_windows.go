package cli

import (
	"os"
	"path/filepath"
)

// noFollowFlags are none: Windows has no FIFOs and no controlling
// terminals, and os.Root is what keeps a link from being followed.
const noFollowFlags = 0

// openNoFollow opens the file at the path with the flags, through the root
// of its folder, which refuses a link at the end of the path as O_NOFOLLOW
// does on Unix.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	return root.OpenFile(filepath.Base(path), flag, perm)
}
