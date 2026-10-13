// Package proxy forwards connections to the internet with HTTP CONNECT.
package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/the127/aibox/internal/tunnel"
)

const (
	dialTimeout      = 30 * time.Second
	handshakeTimeout = 10 * time.Second
	// maxRequestBytes is how much of a CONNECT request the proxy reads, the
	// default limit of Squid for the headers of a request. A real one is a
	// few hundred bytes, and the VM can open many connections at once.
	maxRequestBytes = 64 << 10
	// maxTargets is how many different targets a log keeps, of each kind. A
	// session refuses a handful, so more come from a VM that makes up names,
	// and the rest are only counted.
	maxTargets = 1000
)

// InvalidTarget is what the proxy reports for a request whose target is no
// host and port, instead of its text, which can be as long as the request.
// TooManyConnections is what it reports for a connection beyond
// Options.MaxConnections.
const (
	InvalidTarget      = "an invalid target"
	TooManyConnections = "too many connections at once"
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
	// MaxConnections is how many connections the proxy serves at once, or
	// 0 for any number.
	MaxConnections int
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

// notPublic are IPv4 ranges that IsGlobalUnicast counts as public but
// that belong to the machine's own networks or to nobody: 0.0.0.0/8,
// carrier-grade NAT (which Tailscale uses), the ranges reserved for
// documentation and benchmarks, the relays of 6to4, which is retired, and
// 240.0.0.0/4, which is reserved.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// internet6 is where the public IPv6 addresses are. IPv6 has room for many
// ranges that are not the internet, so an address outside it is refused,
// also one of a range nobody here knew of.
var internet6 = netip.MustParsePrefix("2000::/3")

// notPublic6 are the ranges in internet6 that are not the internet:
// 2001::/23, which IANA keeps for special uses like Teredo, which hides
// IPv4 addresses aibox cannot check, and the ranges reserved for
// documentation.
var notPublic6 = []netip.Prefix{
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("3fff::/20"),
}

// nat64 and sixToFour hold an IPv4 address in their IPv6 addresses, at
// the byte the map says. A network with only IPv6 reaches IPv4 hosts
// through NAT64, so such an address is as public as the IPv4 address in
// it.
var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// Serve accepts connections until the context ends. A connection beyond
// Options.MaxConnections gets a 503 and is closed, so that the client
// learns why instead of waiting.
func Serve(ctx context.Context, listener net.Listener, options Options) error {
	var open chan struct{}
	if options.MaxConnections > 0 {
		open = make(chan struct{}, options.MaxConnections)
	}

	return tunnel.Serve(ctx, listener, func(ctx context.Context, conn net.Conn) {
		if open != nil {
			select {
			case open <- struct{}{}:
				defer func() { <-open }()
			default:
				writeError(conn, http.StatusServiceUnavailable, "aibox: "+TooManyConnections+"\n")

				if options.OnRefused != nil {
					options.OnRefused(TooManyConnections)
				}

				return
			}
		}

		handle(ctx, conn, options)
	})
}

// handle answers one CONNECT request and then joins the client with the
// target.
func handle(ctx context.Context, conn net.Conn, options Options) {
	limited := &io.LimitedReader{R: conn, N: maxRequestBytes}
	reader := bufio.NewReader(limited)

	host, port, local, ok := handshake(conn, reader, limited, options)
	if !ok {
		return
	}

	target := net.JoinHostPort(host, port)

	if local != nil {
		defer options.connected(target)()

		serveLocal(ctx, conn, reader, local)

		return
	}

	upstream, err := connect(ctx, host, port, options)
	if errors.Is(err, errNotPublic) {
		refuse(conn, target, reasonNotPublic, options)

		return
	}

	if err != nil {
		writeError(conn, http.StatusBadGateway, "")

		return
	}

	defer func() { _ = upstream.Close() }()
	defer options.connected(target)()

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
// The limit of limited covers the request only, the tunnel after it reads
// on.
func handshake(conn net.Conn, reader *bufio.Reader, limited *io.LimitedReader, options Options) (host, port string, local http.Handler, ok bool) {
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))

	method, target, err := readConnect(reader)

	switch {
	case err != nil && limited.N == 0:
		writeError(conn, http.StatusRequestHeaderFieldsTooLarge, "")

		return "", "", nil, false
	case err != nil:
		writeError(conn, http.StatusBadRequest, "")

		return "", "", nil, false
	}

	limited.N = math.MaxInt64

	_ = conn.SetReadDeadline(time.Time{})

	if method != http.MethodConnect {
		writeError(conn, http.StatusMethodNotAllowed, "")

		return "", "", nil, false
	}

	host, port, valid := splitTarget(target)
	if !valid {
		writeError(conn, http.StatusBadRequest, "")

		if options.OnRefused != nil {
			options.OnRefused(InvalidTarget)
		}

		return "", "", nil, false
	}

	if options.Local != nil {
		if local = options.Local(host, port); local != nil {
			return host, port, local, true
		}
	}

	if options.Allow != nil && !options.Allow(host, port) {
		refuse(conn, target, reasonNotAllowed, options)

		return "", "", nil, false
	}

	return host, port, nil, true
}

// serveLocal answers the tunnel with the handler until the client closes it
// or the context ends.
func serveLocal(ctx context.Context, conn net.Conn, reader *bufio.Reader, handler http.Handler) {
	writeTunnelOK(conn)

	// the server takes the connection over for a handler, as for a
	// WebSocket, by ending the read it waits on with a deadline in the
	// past. On a Hyper-V socket of Windows that cancels the overlapped
	// receive, after which Winsock leaves the socket unusable. So the
	// server gets one end of a pipe, and the tunnel only plain reads and
	// writes, which carry what the server closed on to the client. A pipe
	// has no half-close, so a client that ends its side ends the server's
	// too, which no local service needs.
	served, relayed := net.Pipe()
	written := make(chan struct{})

	// a write to the pipe returns once the copy read it, not once it
	// reached the client, and the caller closes the tunnel when this
	// returns, so the copy finishes first
	defer func() {
		_ = relayed.Close()
		<-written
	}()

	go func() {
		// the reader may hold bytes the client sent right after its request
		_, _ = io.Copy(relayed, reader)
		_ = relayed.Close()
	}()

	go func() {
		defer close(written)

		_, _ = io.Copy(conn, relayed)
		_ = conn.Close()
	}()

	listener := &oneConn{conn: served, done: make(chan struct{})}

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

// errBadRequestLine is a first line that is not a method, a target and an
// HTTP/1 version.
var errBadRequestLine = errors.New("the request line is not a method, a target and a version")

// readConnect reads a request up to its end and returns the method and the
// target of its first line. It skips the header lines without keeping
// them, since a CONNECT needs none of them, so that a request costs no more
// memory than the reader has.
func readConnect(reader *bufio.Reader) (method, target string, err error) {
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return "", "", err
	}

	method, rest, _ := strings.Cut(strings.TrimRight(string(line), "\r\n"), " ")
	target, version, _ := strings.Cut(rest, " ")

	if method == "" || target == "" || !strings.HasPrefix(version, "HTTP/1.") {
		return "", "", errBadRequestLine
	}

	for {
		header, err := reader.ReadSlice('\n')

		// a header line longer than the buffer comes in parts
		for errors.Is(err, bufio.ErrBufferFull) {
			_, err = reader.ReadSlice('\n')
			header = nil
		}

		if err != nil {
			return "", "", err
		}

		if string(header) == "\r\n" || string(header) == "\n" {
			return method, target, nil
		}
	}
}

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

// ValidTarget says whether the target is a host and port the proxy could
// reach: an address, or a name of at most 253 characters in labels of at
// most 63, and a port.
func ValidTarget(target string) bool {
	_, _, valid := splitTarget(target)

	return valid
}

// splitTarget returns the host and port of a target and whether it is
// valid, as ValidTarget says.
func splitTarget(target string) (host, port string, valid bool) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", "", false
	}

	// as written, so that zeros in front can't make it any length
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", "", false
	}

	if net.ParseIP(host) != nil {
		return host, port, true
	}

	if host == "" || len(host) > 253 {
		return "", "", false
	}

	for label := range strings.SplitSeq(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || strings.ContainsFunc(label, notInLabel) {
			return "", "", false
		}
	}

	return host, port, true
}

// notInLabel says whether a character has no place in a label of a name:
// anything but letters, digits, hyphens and underscores.
func notInLabel(r rune) bool {
	return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_'
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

	// net.ParseIP reads no address with a zone, and the resolver would
	// return the address in it, unchecked, as if it were a name's
	if addr, err := netip.ParseAddr(host); err == nil && addr.Zone() != "" {
		return nil, errNotPublic
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
// not loopback, private, link-local, multicast, unspecified or in notPublic,
// and for IPv6 in internet6 and not in notPublic6. An IPv6 address that
// holds an IPv4 address is as public as that.
func isPublic(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}

	addr = addr.Unmap()

	if inner, ok := heldIPv4(addr); ok {
		addr = inner
	}

	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}

	if addr.Is6() {
		return internet6.Contains(addr) && !slices.ContainsFunc(notPublic6, func(p netip.Prefix) bool { return p.Contains(addr) })
	}

	return !slices.ContainsFunc(notPublic, func(p netip.Prefix) bool { return p.Contains(addr) })
}

// heldIPv4 returns the IPv4 address a NAT64 or 6to4 address holds.
func heldIPv4(addr netip.Addr) (netip.Addr, bool) {
	b := addr.As16()

	switch {
	case nat64.Contains(addr):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixToFour.Contains(addr):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}

	return netip.Addr{}, false
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
	// others counts the uses of targets beyond maxTargets
	others int
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
	if u == nil && len(targets.list) >= maxTargets {
		if targets.others == 0 {
			_, _ = fmt.Fprintf(l.w, "%s %s more than %d targets, the log names no more of them\n", stamp(now), verb, maxTargets)
		}

		targets.others++

		return &use{}
	}

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

	if t.others > 0 {
		_, _ = fmt.Fprintf(w, "%s summary %s other targets %s\n", stamp(now), verb, times(t.others))
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
	// a client that reads nothing must not keep the proxy waiting
	_ = conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))

	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
}
