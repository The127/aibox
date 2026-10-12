// Package hvchild carries the messages between aibox and a helper it
// starts on Windows, over a pair of pipes, in both directions. Each
// message is a frame: its length in four bytes, big-endian, then its JSON.
//
// A helper may run in an AppContainer that a VM took over, so the side
// that reads one trusts nothing it sends: it reads no frame longer than
// the helper's messages can be, so that the helper can't make aibox hold
// more memory than that, and keeps the control characters of the helper's
// text out of what aibox prints.
package hvchild

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

var (
	// ErrTooLong is a frame longer than the reader takes.
	ErrTooLong = errors.New("the message is longer than any the other side sends")
	// ErrClosed is a message that can't reach the other side, as it is
	// gone.
	ErrClosed = errors.New("the other side is gone")
	// ErrBadFrame is a frame that is no message of this package.
	ErrBadFrame = errors.New("a message that is no frame of a message")
	// ErrServed is a Conn served a second time, whose frames two readers
	// would split.
	ErrServed = errors.New("the Conn is served already")
)

// frame is a message as it goes over the pipe.
type frame struct {
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"body,omitempty"`
}

// Conn is one side of the pipes.
type Conn struct {
	r       io.Reader
	limit   uint32
	closers []io.Closer
	served  atomic.Bool

	// a frame goes in one write, and the frames of different writers
	// must not interleave
	writing sync.Mutex
	w       io.Writer
	broken  error
}

// New is the side that reads r and writes w, and closes the closers, which
// close both, when it is closed: none for a side whose pipes end with its
// process. It reads no frame longer than limit bytes, or any frame when
// limit is 0, which is for the side whose other side is aibox itself.
func New(r io.Reader, w io.Writer, limit uint32, closers ...io.Closer) *Conn {
	return &Conn{r: r, w: w, closers: closers, limit: limit}
}

// Close closes the pipes, which ends Serve and a write that waits for the
// other side to read.
func (c *Conn) Close() error {
	errs := make([]error, 0, len(c.closers))
	for _, closer := range c.closers {
		errs = append(errs, closer.Close())
	}

	return errors.Join(errs...)
}

// Send sends a message of the kind with the body.
func (c *Conn) Send(kind string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}

	return c.write(frame{Kind: kind, Body: raw})
}

// Size is the length of the frame of a message of the kind with the body,
// which bounds what the reader of such messages takes.
func Size(kind string, body any) (uint32, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}

	f, err := json.Marshal(frame{Kind: kind, Body: raw})
	if err != nil {
		return 0, err
	}

	if uint64(len(f)) > uint64(^uint32(0)) {
		return 0, fmt.Errorf("a message of %d bytes, more than a frame holds", len(f))
	}

	return uint32(len(f)), nil //nolint:gosec // checked above
}

// Printable is the text without control characters, which could move the
// cursor or forge a line where aibox prints what a helper said.
func Printable(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}

		return r
	}, text)
}

// Serve gives each message the other side sends to message, in the order
// they come, until the other side is gone, the Conn is closed or a frame
// is wrong, which it returns, or nil once the other side ended. message
// runs while nothing else is read, so it must not wait. It runs once.
func (c *Conn) Serve(message func(kind string, body json.RawMessage)) error {
	if c.served.Swap(true) {
		return ErrServed
	}

	for {
		f, err := c.read()
		// the other side ends between frames, which is how it is meant to
		// end, no failure of it
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		if f.Kind == "" {
			return ErrBadFrame
		}

		message(f.Kind, f.Body)
	}
}

func (c *Conn) write(f frame) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}

	if uint64(len(raw)) > uint64(^uint32(0)) {
		return fmt.Errorf("a message of %d bytes, more than a frame holds", len(raw))
	}

	buf := make([]byte, 4, 4+len(raw))
	binary.BigEndian.PutUint32(buf, uint32(len(raw))) //nolint:gosec // checked above
	buf = append(buf, raw...)

	c.writing.Lock()
	defer c.writing.Unlock()

	// after a write that failed part way, the other side would take the
	// rest of the stream for frames that aren't
	if c.broken != nil {
		return c.broken
	}

	if _, err := c.w.Write(buf); err != nil {
		c.broken = fmt.Errorf("%w: %w", ErrClosed, err)

		return c.broken
	}

	return nil
}

func (c *Conn) read() (frame, error) {
	var head [4]byte
	if _, err := io.ReadFull(c.r, head[:]); err != nil {
		return frame{}, err
	}

	n := binary.BigEndian.Uint32(head[:])
	if c.limit != 0 && n > c.limit {
		return frame{}, fmt.Errorf("%w: %d bytes, more than %d", ErrTooLong, n, c.limit)
	}

	// read as it comes rather than trust the length for the memory up front
	raw, err := io.ReadAll(io.LimitReader(c.r, int64(n)))
	if err != nil {
		return frame{}, err
	}

	if uint32(len(raw)) != n { //nolint:gosec // at most n, a uint32
		return frame{}, io.ErrUnexpectedEOF
	}

	var f frame
	if err := json.Unmarshal(raw, &f); err != nil {
		// the frame itself may hold a secret, so the error names none of it
		return frame{}, ErrBadFrame
	}

	return f, nil
}
