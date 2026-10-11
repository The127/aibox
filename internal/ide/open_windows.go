package ide

import "os"

// openLock opens a lock file. Windows has no FIFOs that opening would wait
// on.
func openLock(path string) (*os.File, error) {
	return os.Open(path) //nolint:gosec // the path is a file in the folder of the lock files
}
