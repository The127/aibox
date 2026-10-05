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

// kindTimeout is how long a new stream may take to name its kind, and
// waitTimeout how long it may wait to be accepted, so that a stream the
// guest opens for nothing holds nothing up for long.
const (
	kindTimeout = 10 * time.Second
	waitTimeout = 10 * time.Second
)

// maxWaiting is how many streams the host takes in before they are accepted.
// The guest is not trusted, and each stream may hold a window of data, so
// yamux turns away the streams beyond these and its backlog.
const maxWaiting = 16

func config() *yamux.Config {
	c := yamux.DefaultConfig()
	c.AcceptBacklog = maxWaiting
	c.LogOutput = io.Discard

	return c
}

// HostEnd is the end of the link on the host. It accepts the streams the
// guest opens.
type HostEnd struct {
	session  *yamux.Session
	kind     time.Duration
	wait     time.Duration
	waiting  chan struct{}
	terminal *listener
	proxy    *listener
}

// Host starts the host end of a link over the connection.
func Host(conn net.Conn) (*HostEnd, error) {
	return host(conn, kindTimeout, waitTimeout)
}

func host(conn net.Conn, kind, wait time.Duration) (*HostEnd, error) {
	session, err := yamux.Server(conn, config())
	if err != nil {
		return nil, err
	}

	h := &HostEnd{
		session:  session,
		kind:     kind,
		wait:     wait,
		waiting:  make(chan struct{}, maxWaiting),
		terminal: newListener(session.Addr()),
		proxy:    newListener(session.Addr()),
	}

	go h.route()

	return h, nil
}

// Terminal accepts the terminal streams of the guest. Accept fails with
// net.ErrClosed once the link is gone, as it does after Close.
func (h *HostEnd) Terminal() net.Listener { return h.terminal }

// Proxy accepts the proxy streams of the guest. Accept fails with
// net.ErrClosed once the link is gone, as it does after Close.
func (h *HostEnd) Proxy() net.Listener { return h.proxy }

// Close ends the link and every stream on it.
func (h *HostEnd) Close() error { return h.session.Close() }

func (h *HostEnd) route() {
	defer func() {
		_ = h.terminal.Close()
		_ = h.proxy.Close()
	}()

	for {
		// while maxWaiting streams wait, new ones queue in the backlog of
		// yamux, which resets those beyond it
		select {
		case h.waiting <- struct{}{}:
		case <-h.session.CloseChan():
			return
		}

		stream, err := h.session.AcceptStream()
		if err != nil {
			return
		}

		// a stream that is slow to name its kind must not hold up the next
		go func() {
			h.sort(halfClosing{stream})
			<-h.waiting
		}()
	}
}

func (h *HostEnd) sort(stream halfClosing) {
	kind := make([]byte, 1)

	_ = stream.SetReadDeadline(time.Now().Add(h.kind))
	if _, err := io.ReadFull(stream, kind); err != nil {
		_ = stream.Close()

		return
	}

	_ = stream.SetReadDeadline(time.Time{})

	switch kind[0] {
	case kindTerminal:
		h.terminal.offer(stream, h.wait)
	case kindProxy:
		h.proxy.offer(stream, h.wait)
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

func (l *listener) offer(stream net.Conn, wait time.Duration) {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case l.streams <- stream:
	case <-l.done:
		_ = stream.Close()
	case <-timer.C:
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

	conn := halfClosing{stream}
	if _, err := conn.Write([]byte{kind}); err != nil {
		_ = conn.Close()

		return nil, err
	}

	return conn, nil
}

// halfClosing is a stream that can stop sending and go on receiving, so that
// the tunnel passes a close on as it does for TCP. Closing a yamux stream
// ends only the sending side and leaves a read waiting until the other side
// closes too. Close here ends the reading side as well, because whoever
// closes a connection waits for its reads to end.
type halfClosing struct {
	*yamux.Stream
}

func (s halfClosing) CloseWrite() error { return s.Stream.Close() }

func (s halfClosing) Close() error {
	_ = s.SetReadDeadline(time.Now())

	return s.Stream.Close()
}
