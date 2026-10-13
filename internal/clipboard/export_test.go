package clipboard

import (
	"bufio"
	"io"
)

// ServeWith runs the helper with read in place of the clipboard.
func ServeWith(in io.Reader, out io.Writer, read func() ([]byte, error)) error {
	return serve(in, out, read, func() ([]byte, error) { return nil, ErrNoText })
}

// HelperOn is a helper that asks over in and reads its answers from out.
func HelperOn(in io.WriteCloser, out io.Reader) *Helper {
	return &Helper{in: in, out: bufio.NewReader(out)}
}

// ServeBothWith runs the helper with read and readText in place of the
// clipboard.
func ServeBothWith(in io.Reader, out io.Writer, read, readText func() ([]byte, error)) error {
	return serve(in, out, read, readText)
}
