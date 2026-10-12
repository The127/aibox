//go:build !windows

package realpath

import "path/filepath"

// Of is the path the file at path is at, with every link followed. The
// file must exist.
func Of(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
