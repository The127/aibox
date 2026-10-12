// Package realpath finds the path a file is at once every link on the way
// to it is followed: symlinks, and on Windows junctions and mount points
// too, which filepath.EvalSymlinks leaves as they are since Go 1.23. A
// check that a path stays inside a folder must see where it leads, or a
// junction takes it out unseen.
package realpath

import "path/filepath"

// Resolved is the path with every link followed, or, where that fails,
// for example because it does not exist yet, the path as it is, cleaned.
func Resolved(path string) string {
	if target, err := Of(path); err == nil {
		return target
	}

	return filepath.Clean(path)
}
