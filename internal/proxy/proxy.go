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
	"strconv"
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
	// OnClosed is called with the host:port of each request the proxy
	// reached once its tunnel has closed.
	OnClosed func(target string)
	// Hint returns a line added to each refusal of the target, such as
	// where to allow it, or nothing.
	Hint func(target string) string
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
		defer options.connected(net.JoinHostPort(host, port))()

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
	defer options.connected(net.JoinHostPort(host, port))()

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

// connected reports the target as reached and returns what reports it
// closed.
func (o Options) connected(target string) func() {
	if o.OnConnected != nil {
		o.OnConnected(target)
	}

	return func() {
		if o.OnClosed != nil {
			o.OnClosed(target)
		}
	}
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

	// a handler that takes the connection over, as for a WebSocket, goes on
	// using it after the server is done with it, so the tunnel stays open
	// until every handler has returned. Once the server is done no handler
	// may start, or it could start after the wait.
	var (
		handlers sync.WaitGroup
		mu       sync.Mutex
		done     bool
	)

	defer func() {
		mu.Lock()
		done = true
		mu.Unlock()
		handlers.Wait()
	}()

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if done {
				mu.Unlock()
				http.Error(w, "the tunnel is closing", http.StatusServiceUnavailable)

				return
			}

			handlers.Add(1)
			mu.Unlock()

			defer handlers.Done()

			handler.ServeHTTP(w, r)
		}),
		BaseContext:       func(net.Listener) context.Context { return ctx },
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
	if reason == reasonNotAllowed && options.Hint != nil {
		if hint := options.Hint(target); hint != "" {
			body += hint + "\n"
		}
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

// Log writes each target the proxy connected to or refused to a writer
// once, and keeps them with how often and when last they were used. It
// writes what the guest asked for, not what went through.
type Log struct {
	mu        sync.Mutex
	w         io.Writer
	now       func() time.Time
	connected targets
	refused   targets
}

type targets struct {
	list []string
	uses map[string]*use
}

// use is how often a target was used, and when last: when its last
// connection closed, or when it was last refused.
type use struct {
	count int
	last  time.Time
}

// NewLog returns a Log that writes to w.
func NewLog(w io.Writer) *Log {
	return &Log{w: w, now: time.Now}
}

// Connected logs a target the proxy connected to. It is an OnConnected
// function.
func (l *Log) Connected(target string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.add("connected", target, &l.connected, l.now())
}

// Closed notes when a connection to a target ended. It is an OnClosed
// function.
func (l *Log) Closed(target string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if u := l.connected.uses[target]; u != nil {
		u.last = l.now()
	}
}

// Refused logs a target the proxy refused. It is an OnRefused function.
func (l *Log) Refused(target string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.add("refused", target, &l.refused, now).last = now
}

// Targets returns the targets the proxy connected to and those it refused,
// each in the order the proxy first met them.
func (l *Log) Targets() (connected, refused []string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.Clone(l.connected.list), slices.Clone(l.refused.list)
}

// Summarize writes for each target how often the proxy connected to it or
// refused it, and when last. Call it once the proxy has stopped, so that no
// connection is still open.
func (l *Log) Summarize() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.connected.summarize(l.w, now, "connected", "last closed at")
	l.refused.summarize(l.w, now, "refused", "last at")
}

// add counts a use of the target and writes the target the first time.
// The caller holds l.mu.
func (l *Log) add(verb, target string, targets *targets, now time.Time) *use {
	u := targets.uses[target]
	if u == nil {
		if targets.uses == nil {
			targets.uses = map[string]*use{}
		}

		u = &use{}
		targets.uses[target] = u
		targets.list = append(targets.list, target)

		_, _ = fmt.Fprintf(l.w, "%s %s %q\n", stamp(now), verb, target)
	}

	u.count++

	return u
}

func (t *targets) summarize(w io.Writer, now time.Time, verb, last string) {
	for _, target := range t.list {
		u := t.uses[target]
		_, _ = fmt.Fprintf(w, "%s summary %s %q %s, %s %s\n", stamp(now), verb, target, times(u.count), last, stamp(u.last))
	}
}

func times(n int) string {
	if n == 1 {
		return "1 time"
	}

	return strconv.Itoa(n) + " times"
}

func stamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
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
