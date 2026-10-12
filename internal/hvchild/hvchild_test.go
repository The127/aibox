package hvchild_test

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hvchild"
)

// recorder keeps the messages it got.
type recorder struct {
	mu       sync.Mutex
	messages []string
	got      chan struct{}
}

func newRecorder() *recorder {
	return &recorder{got: make(chan struct{}, 64)}
}

func (r *recorder) message(kind string, body json.RawMessage) {
	r.mu.Lock()
	r.messages = append(r.messages, kind+" "+string(body))
	r.mu.Unlock()

	r.got <- struct{}{}
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.messages...)
}

func ignore(string, json.RawMessage) {}

func TestAMessageReachesTheOtherSide(t *testing.T) {
	// arrange: a, the side that trusts, and b, which reads no frame longer
	// than a KiB
	ab, ba := net.Pipe()
	a := hvchild.New(ab, ab, 0, ab)
	b := hvchild.New(ba, ba, 1<<10, ba)
	rb := newRecorder()

	go func() { _ = b.Serve(rb.message) }()

	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})

	// act
	require.NoError(t, a.Send("connected", map[string]string{"target": "github.com:443"}))

	// assert
	select {
	case <-rb.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}

	assert.Equal(t, []string{`connected {"target":"github.com:443"}`}, rb.all())
}

func TestAFrameUpToTheLimitIsTakenAndOneBeyondEndsTheReader(t *testing.T) {
	// arrange: frames written by hand, as long as asked for
	ab, ba := net.Pipe()
	b := hvchild.New(ba, ba, 64, ba)
	t.Cleanup(func() { _ = ab.Close() })
	rb := newRecorder()
	served := make(chan error, 1)

	go func() { served <- b.Serve(rb.message) }()

	write := func(n int) {
		// a message whose JSON is n bytes long
		body := `{"kind":"k","body":"` + strings.Repeat("x", n-len(`{"kind":"k","body":""}`)) + `"}`

		var head [4]byte
		binary.BigEndian.PutUint32(head[:], uint32(len(body))) //nolint:gosec // short
		_, _ = ab.Write(append(head[:], body...))
	}

	// act
	write(64)
	<-rb.got

	go write(65)

	// assert
	select {
	case err := <-served:
		assert.ErrorIs(t, err, hvchild.ErrTooLong)
	case <-time.After(5 * time.Second):
		t.Fatal("the long frame was taken")
	}

	assert.Len(t, rb.all(), 1, "the frame of the limit")
}

func TestServeEndsWhenTheOtherSideIsGoneAndMessagesFailAfter(t *testing.T) {
	// arrange
	ab, ba := net.Pipe()
	a := hvchild.New(ab, ab, 0, ab)
	served := make(chan error, 1)

	go func() { served <- a.Serve(ignore) }()

	// act
	require.NoError(t, ba.Close())

	// assert
	assert.NoError(t, <-served, "the other side ended between frames, as it is meant to")
	assert.ErrorIs(t, a.Send("k", nil), hvchild.ErrClosed)
}

func TestAConnIsServedOnce(t *testing.T) {
	// arrange: served once, until its other side is gone
	ab, ba := net.Pipe()
	require.NoError(t, ba.Close())

	a := hvchild.New(ab, ab, 0, ab)
	require.NoError(t, a.Serve(ignore))

	// act
	err := a.Serve(ignore)

	// assert
	assert.ErrorIs(t, err, hvchild.ErrServed)
}

func TestAFrameThatIsNoMessageIsRefusedWithoutWhatItHolds(t *testing.T) {
	tests := map[string]string{
		"no JSON":            "sk-ant-the-secret",
		"JSON of no message": `{"body":"sk-ant-the-secret"}`,
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			ab, ba := net.Pipe()
			b := hvchild.New(ba, ba, 1<<10, ba)
			served := make(chan error, 1)

			go func() { served <- b.Serve(ignore) }()

			var head [4]byte
			binary.BigEndian.PutUint32(head[:], uint32(len(content))) //nolint:gosec // short

			// act
			_, err := ab.Write(append(head[:], content...))
			require.NoError(t, err)

			// assert
			err = <-served
			require.ErrorIs(t, err, hvchild.ErrBadFrame)
			assert.NotContains(t, err.Error(), "sk-ant")
		})
	}
}

func TestClosingTheConnEndsAWriteTheOtherSideDoesNotRead(t *testing.T) {
	// arrange: nobody reads the other end
	ab, _ := net.Pipe()
	a := hvchild.New(ab, ab, 0, ab)

	sent := make(chan error, 1)

	go func() { sent <- a.Send("k", "x") }()

	// act
	require.NoError(t, a.Close())

	// assert
	select {
	case err := <-sent:
		assert.ErrorIs(t, err, hvchild.ErrClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("the write outlived Close")
	}
}

func TestAnEndInsideAFrameIsAFailure(t *testing.T) {
	// arrange: the other side sends half a frame and hangs up
	ab, ba := net.Pipe()
	a := hvchild.New(ab, ab, 0, ab)
	served := make(chan error, 1)

	go func() { served <- a.Serve(ignore) }()

	// act
	_, err := ba.Write([]byte{0, 0, 0, 9, '{'})
	require.NoError(t, err)
	require.NoError(t, ba.Close())

	// assert
	assert.ErrorIs(t, <-served, io.ErrUnexpectedEOF)
}

func TestPrintableKeepsNoControlCharacters(t *testing.T) {
	assert.Equal(t, "refused ?[31mred?[0m?forged line", hvchild.Printable("refused \x1b[31mred\x1b[0m\nforged line"))
}

func TestSizeIsTheLengthOfTheFrameOfAMessage(t *testing.T) {
	// arrange
	ab, ba := net.Pipe()
	a := hvchild.New(ab, ab, 0, ab)
	t.Cleanup(func() { _ = a.Close() })

	size, err := hvchild.Size("proxy", map[string]string{"target": "<github.com>:443"})
	require.NoError(t, err)

	// act: the frame as it goes over the pipe
	go func() { _ = a.Send("proxy", map[string]string{"target": "<github.com>:443"}) }()

	var head [4]byte
	_, err = io.ReadFull(ba, head[:])
	require.NoError(t, err)

	// assert
	assert.Equal(t, binary.BigEndian.Uint32(head[:]), size)
}
