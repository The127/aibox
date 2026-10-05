// Package link carries the terminal and the proxy of the VM over a single
// connection between host and guest, whichever of the two made it. The
// guest opens a stream for each connection it needs and names its kind,
// and the host hands each stream to the listener of that kind.
package link

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	kindTerminal byte = 't'
	kindProxy    byte = 'p'
)

// kindTimeout is how long a new stream may take to name its kind, so that a
// stream that never does holds nothing up for long.
const kindTimeout = 10 * time.Second

func config() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard

	return c
}

// HostEnd is the end of the link on the host. It accepts the streams the
// guest opens.
type HostEnd struct {
	session  *yamux.Session
	terminal *listener
	proxy    *listener
}

// Host starts the host end of a link over the connection.
func Host(conn net.Conn) (*HostEnd, error) {
	session, err := yamux.Server(conn, config())
	if err != nil {
		return nil, err
	}

	h := &HostEnd{
		session:  session,
		terminal: newListener(session.Addr()),
		proxy:    newListener(session.Addr()),
	}

	go h.route()

	return h, nil
}

// Terminal accepts the terminal streams of the guest.
func (h *HostEnd) Terminal() net.Listener { return h.terminal }

// Proxy accepts the proxy streams of the guest.
func (h *HostEnd) Proxy() net.Listener { return h.proxy }

// Close ends the link and every stream on it.
func (h *HostEnd) Close() error { return h.session.Close() }

func (h *HostEnd) route() {
	for {
		stream, err := h.session.AcceptStream()
		if err != nil {
			_ = h.terminal.Close()
			_ = h.proxy.Close()

			return
		}

		// a stream that is slow to name its kind must not hold up the next
		go h.sort(stream)
	}
}

func (h *HostEnd) sort(stream *yamux.Stream) {
	kind := make([]byte, 1)

	_ = stream.SetReadDeadline(time.Now().Add(kindTimeout))
	if _, err := io.ReadFull(stream, kind); err != nil {
		_ = stream.Close()

		return
	}

	_ = stream.SetReadDeadline(time.Time{})

	switch kind[0] {
	case kindTerminal:
		h.terminal.offer(halfClosing{stream})
	case kindProxy:
		h.proxy.offer(halfClosing{stream})
	default:
		_ = stream.Close()
	}
}

// listener hands out the streams of one kind.
type listener struct {
	addr    net.Addr
	streams chan net.Conn
	done    chan struct{}
	once    sync.Once
}

func newListener(addr net.Addr) *listener {
	return &listener{addr: addr, streams: make(chan net.Conn), done: make(chan struct{})}
}

func (l *listener) offer(stream net.Conn) {
	select {
	case l.streams <- stream:
	case <-l.done:
		_ = stream.Close()
	}
}

func (l *listener) Accept() (net.Conn, error) {
	select {
	case stream := <-l.streams:
		return stream, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *listener) Close() error {
	l.once.Do(func() { close(l.done) })

	return nil
}

func (l *listener) Addr() net.Addr { return l.addr }

// GuestEnd is the end of the link in the guest. It opens a stream for each
// connection to the host.
type GuestEnd struct {
	session *yamux.Session
}

// Guest starts the guest end of a link over the connection.
func Guest(conn net.Conn) (*GuestEnd, error) {
	session, err := yamux.Client(conn, config())
	if err != nil {
		return nil, err
	}

	return &GuestEnd{session: session}, nil
}

// DialTerminal opens a stream to the terminal of the host.
func (g *GuestEnd) DialTerminal() (net.Conn, error) { return g.dial(kindTerminal) }

// DialProxy opens a stream to the proxy of the host.
func (g *GuestEnd) DialProxy() (net.Conn, error) { return g.dial(kindProxy) }

// Close ends the link and every stream on it.
func (g *GuestEnd) Close() error { return g.session.Close() }

func (g *GuestEnd) dial(kind byte) (net.Conn, error) {
	stream, err := g.session.OpenStream()
	if err != nil {
		return nil, err
	}

	if _, err := stream.Write([]byte{kind}); err != nil {
		_ = stream.Close()

		return nil, err
	}

	return halfClosing{stream}, nil
}

// halfClosing is a stream that says it can stop sending and go on
// receiving, so that the tunnel passes a close on as it does for TCP. Closing
// a yamux stream does exactly that, it ends only the sending side.
type halfClosing struct {
	*yamux.Stream
}

func (s halfClosing) CloseWrite() error { return s.Close() }
