// Package proxy forwards connections to the internet with HTTP CONNECT.
package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/the127/aibox/internal/tunnel"
)

const (
	dialTimeout      = 30 * time.Second
	handshakeTimeout = 10 * time.Second
)

// Options control what the proxy lets through.
type Options struct {
	// Allow is called with the host name and port of each CONNECT request
	// and returns whether they may be reached. Nil allows everything.
	Allow func(host, port string) bool
	// OnConnected is called with the host:port of each request once the
	// proxy reached it, before the tunnel opens.
	OnConnected func(target string)
	// OnRefused is called with the host:port of each refused request.
	OnRefused func(target string)
	// Hint is a line added to each refusal, such as where to allow the host.
	Hint string
	// Resolve looks a host name up. Nil uses the system resolver.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
	// Local returns the handler that answers a target itself, over HTTP in
	// the tunnel, or nil for a target the proxy connects to. A target it
	// answers needs no place on the allow list.
	Local func(host, port string) http.Handler
	// Pinned returns the addresses a name stands for without asking DNS,
	// such as localhost. They are dialed even when they are not public. Nil,
	// or no addresses, resolves the name as usual.
	Pinned func(host, port string) []net.IP
}

// errNotPublic is a name whose addresses all lie in the networks of the
// machine aibox runs on.
var errNotPublic = errors.New("no public address")

// The reasons a refusal names.
const (
	reasonNotAllowed = "is not on the allow list"
	reasonNotPublic  = "resolves only to addresses inside the host's own networks"
)

// notPublic are address ranges that IsGlobalUnicast counts as public but
// that belong to the machine's own networks or to nobody: 0.0.0.0/8,
// carrier-grade NAT (which Tailscale uses), and the ranges reserved for
// documentation and benchmarks.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
}

// Serve accepts connections until the context ends.
func Serve(ctx context.Context, listener net.Listener, options Options) error {
	return tunnel.Serve(ctx, listener, func(ctx context.Context, conn net.Conn) {
		handle(ctx, conn, options)
	})
}

// handle answers one CONNECT request and then joins the client with the
// target.
func handle(ctx context.Context, conn net.Conn, options Options) {
	reader := bufio.NewReader(conn)

	host, port, local, ok := handshake(conn, reader, options)
	if !ok {
		return
	}

	if local != nil {
		if options.OnConnected != nil {
			options.OnConnected(net.JoinHostPort(host, port))
		}

		serveLocal(ctx, conn, reader, local)

		return
	}

	upstream, err := connect(ctx, host, port, options)
	if errors.Is(err, errNotPublic) {
		refuse(conn, net.JoinHostPort(host, port), reasonNotPublic, options)

		return
	}

	if err != nil {
		writeError(conn, http.StatusBadGateway, "")

		return
	}

	defer func() { _ = upstream.Close() }()

	if options.OnConnected != nil {
		options.OnConnected(net.JoinHostPort(host, port))
	}

	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stop()

	writeTunnelOK(conn)

	// the reader may hold bytes the client sent right after its request
	if n := reader.Buffered(); n > 0 {
		head, _ := reader.Peek(n)
		if _, err := upstream.Write(head); err != nil {
			return
		}
	}

	tunnel.Join(conn, upstream)
}

// handshake reads the CONNECT request and returns the host and port to
// reach, or the handler that answers them. It answers the client itself
// when there is nothing to reach.
func handshake(conn net.Conn, reader *bufio.Reader, options Options) (host, port string, local http.Handler, ok bool) {
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))

	request, err := http.ReadRequest(reader)
	if err != nil {
		writeError(conn, http.StatusBadRequest, "")

		return "", "", nil, false
	}

	_ = conn.SetReadDeadline(time.Time{})

	if request.Method != http.MethodConnect {
		writeError(conn, http.StatusMethodNotAllowed, "")

		return "", "", nil, false
	}

	host, port, err = net.SplitHostPort(request.Host)
	if err != nil || host == "" || port == "" {
		writeError(conn, http.StatusBadRequest, "")

		return "", "", nil, false
	}

	if options.Local != nil {
		if local = options.Local(host, port); local != nil {
			return host, port, local, true
		}
	}

	if options.Allow != nil && !options.Allow(host, port) {
		refuse(conn, request.Host, reasonNotAllowed, options)

		return "", "", nil, false
	}

	return host, port, nil, true
}

// serveLocal answers the tunnel with the handler until the client closes it
// or the context ends.
func serveLocal(ctx context.Context, conn net.Conn, reader *bufio.Reader, handler http.Handler) {
	writeTunnelOK(conn)

	// the reader may hold bytes the client sent right after its request
	listener := &oneConn{conn: bufferedConn{Conn: conn, reader: reader}, done: make(chan struct{})}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: handshakeTimeout,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed || state == http.StateHijacked {
				_ = listener.Close()
			}
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}

	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()

	_ = server.Serve(listener)
}

// bufferedConn reads what the reader of the handshake holds before the rest
// of the connection.
type bufferedConn struct {
	net.Conn

	reader *bufio.Reader
}

func (c bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

// oneConn is a listener of a single connection. Accept returns it once and
// then waits until it is closed.
type oneConn struct {
	conn     net.Conn
	accepted bool
	once     sync.Once
	done     chan struct{}
}

func (l *oneConn) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true

		return l.conn, nil
	}

	<-l.done

	return nil, net.ErrClosed
}

func (l *oneConn) Close() error {
	l.once.Do(func() { close(l.done) })

	return nil
}

func (l *oneConn) Addr() net.Addr { return l.conn.LocalAddr() }

// refuse answers with a 403 that says why, so that the program inside the
// VM can report it. The hint says where to allow the host, which only helps
// when the host is missing from the list.
func refuse(conn net.Conn, target, reason string, options Options) {
	body := "aibox: " + target + " " + reason + "\n"
	if reason == reasonNotAllowed && options.Hint != "" {
		body += options.Hint + "\n"
	}

	writeError(conn, http.StatusForbidden, body)

	if options.OnRefused != nil {
		options.OnRefused(target)
	}
}

// connect reaches the host on the port. An address in the request is dialed
// as given, and so are the pinned addresses of a name. Any other name is
// resolved here, and only its public addresses are dialed, so that a name
// cannot lead into the machine's own networks and a changed DNS answer
// cannot lead elsewhere than the checked address.
func connect(ctx context.Context, host, port string, options Options) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	var pinned []net.IP
	if options.Pinned != nil {
		pinned = options.Pinned(host, port)
	}

	addresses := []net.IP{net.ParseIP(host)}

	switch {
	case len(pinned) > 0:
		addresses = pinned
	case addresses[0] == nil:
		resolve := options.Resolve
		if resolve == nil {
			resolve = lookup
		}

		all, err := resolve(ctx, host)
		if err != nil {
			return nil, err
		}

		addresses = slices.DeleteFunc(all, func(ip net.IP) bool { return !isPublic(ip) })
		if len(addresses) == 0 {
			return nil, errNotPublic
		}
	}

	var (
		dialer net.Dialer
		err    error
	)

	for _, address := range addresses {
		var conn net.Conn

		conn, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), port))
		if err == nil {
			return conn, nil
		}
	}

	return nil, err
}

func lookup(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// isPublic says whether an address lies outside the machine's own networks:
// not loopback, private, link-local, multicast, unspecified or in notPublic.
func isPublic(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}

	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}

	return !slices.ContainsFunc(notPublic, func(p netip.Prefix) bool { return p.Contains(addr) })
}

// maxLoggedTargets is how many targets of each kind a Log keeps, so that a
// guest that asks for ever new ones cannot fill the log or the memory. The
// kinds count apart, so that refused names, which a guest can make up
// without end, cannot crowd out the targets the proxy connected to.
const maxLoggedTargets = 1000

// Log writes each target the proxy connected to or refused to a writer
// once, so that a guest that keeps trying cannot fill the log, and keeps
// them. It writes what the guest asked for, not what went through.
type Log struct {
	mu        sync.Mutex
	w         io.Writer
	connected targets
	refused   targets
}

// Targets are the targets of one kind, in the order the proxy first met
// them. Full says that later ones were left out.
type Targets struct {
	List []string
	Full bool
}

// targets are the Targets of one kind a Log keeps, and those it saw.
type targets struct {
	Targets

	seen map[string]bool
}

// NewLog returns a Log that writes to w.
func NewLog(w io.Writer) *Log {
	return &Log{w: w}
}

// Connected logs a target the proxy connected to. It is an OnConnected
// function.
func (l *Log) Connected(target string) {
	l.add("connected", target, &l.connected)
}

// Refused logs a target the proxy refused. It is an OnRefused function.
func (l *Log) Refused(target string) {
	l.add("refused", target, &l.refused)
}

// Targets returns the targets the proxy connected to and those it refused.
func (l *Log) Targets() (connected, refused Targets) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return Targets{List: slices.Clone(l.connected.List), Full: l.connected.Full},
		Targets{List: slices.Clone(l.refused.List), Full: l.refused.Full}
}

func (l *Log) add(verb, target string, targets *targets) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if targets.seen[target] {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	if len(targets.List) >= maxLoggedTargets {
		if !targets.Full {
			targets.Full = true
			_, _ = fmt.Fprintf(l.w, "%s %s %d targets, later ones are left out\n", now, verb, maxLoggedTargets)
		}

		return
	}

	if targets.seen == nil {
		targets.seen = map[string]bool{}
	}

	targets.seen[target] = true
	targets.List = append(targets.List, target)

	_, _ = fmt.Fprintf(l.w, "%s %s %q\n", now, verb, target)
}

// writeTunnelOK answers a CONNECT. The reply has no headers about a body,
// because the bytes that follow are the tunnel.
func writeTunnelOK(conn net.Conn) {
	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
}

func writeError(conn net.Conn, status int, body string) {
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
}
