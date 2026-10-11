//go:build !linux && !(darwin && cgo)

package fence

import (
	"errors"
	"io"
)

// a host without a backend runs no VM that could have written the folder.
// Windows gets the helper with its backend.
const confines = false

var errNoHelper = errors.New("aibox has no helper on this kind of host")

// Serve is the helper, which this kind of host does not have.
func Serve([]string, io.Reader) error { return errNoHelper }
