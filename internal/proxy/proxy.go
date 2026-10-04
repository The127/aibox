// Package proxy forwards connections to the internet with HTTP CONNECT.
package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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
	// OnRefused is called with the host:port of each refused request.
	OnRefused func(target string)
	// Hint is a line added to each refusal, such as where to allow the host.
	Hint string
	// Resolve looks a host name up. Nil uses the system resolver.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
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

	host, port, ok := handshake(conn, reader, options)
	if !ok {
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
// reach. It answers the client itself when there is nothing to reach.
func handshake(conn net.Conn, reader *bufio.Reader, options Options) (host, port string, ok bool) {
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))

	request, err := http.ReadRequest(reader)
	if err != nil {
		writeError(conn, http.StatusBadRequest, "")

		return "", "", false
	}

	_ = conn.SetReadDeadline(time.Time{})

	if request.Method != http.MethodConnect {
		writeError(conn, http.StatusMethodNotAllowed, "")

		return "", "", false
	}

	host, port, err = net.SplitHostPort(request.Host)
	if err != nil || host == "" || port == "" {
		writeError(conn, http.StatusBadRequest, "")

		return "", "", false
	}

	if options.Allow != nil && !options.Allow(host, port) {
		refuse(conn, request.Host, reasonNotAllowed, options)

		return "", "", false
	}

	return host, port, true
}

// refuse answers with a 403 that says why, so that the program inside the
// VM can report it.
func refuse(conn net.Conn, target, reason string, options Options) {
	body := "aibox: " + target + " " + reason + "\n"
	if options.Hint != "" {
		body += options.Hint + "\n"
	}

	writeError(conn, http.StatusForbidden, body)

	if options.OnRefused != nil {
		options.OnRefused(target)
	}
}

// connect reaches the host on the port. An address in the request is dialed
// as given. A name is resolved here, and only its public addresses are
// dialed, so that a name cannot lead into the machine's own networks and a
// changed DNS answer cannot lead elsewhere than the checked address.
func connect(ctx context.Context, host, port string, options Options) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	addresses := []net.IP{net.ParseIP(host)}
	if addresses[0] == nil {
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

// RefusalLog returns an OnRefused function that writes each refused target
// to the writer once, so that a guest that keeps trying cannot fill the log.
func RefusalLog(w io.Writer) func(target string) {
	var mu sync.Mutex

	seen := map[string]bool{}

	return func(target string) {
		mu.Lock()
		defer mu.Unlock()

		if seen[target] {
			return
		}

		seen[target] = true

		_, _ = fmt.Fprintf(w, "%s refused %q\n", time.Now().UTC().Format(time.RFC3339), target)
	}
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
