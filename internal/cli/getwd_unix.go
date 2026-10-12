//go:build !windows

package cli

import "os"

// getwd is the current folder as the shell named it: a project reached
// through a symlink keeps the folder it always had.
func getwd() (string, error) { return os.Getwd() }

// spelled is the path as getwd would spell it: as it is.
func spelled(path string) string { return path }
