//go:build !windows

package machine

import "os"

// markSparse does nothing: a Unix file system writes only the blocks that
// were written to.
func markSparse(*os.File) error { return nil }
