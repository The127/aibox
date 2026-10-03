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
	// Allow is called with the host name of each CONNECT request and
	// returns whether it may be reached. Nil allows every host.
	Allow func(host string) bool
	// OnRefused is called with the host name of each refused request.
	OnRefused func(host string)
	// Resolve looks a host name up. Nil uses the system resolver.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
}

// errNotPublic is a name whose addresses all lie in the networks of the
// machine aibox runs on.
var errNotPublic = errors.New("no public address")

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
		refuse(conn, host, options)

		return
	}

	if err != nil {
		writeStatus(conn, http.StatusBadGateway)

		return
	}

	defer func() { _ = upstream.Close() }()

	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stop()

	writeStatus(conn, http.StatusOK)

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
		return "", "", false
	}

	_ = conn.SetReadDeadline(time.Time{})

	if request.Method != http.MethodConnect {
		writeStatus(conn, http.StatusMethodNotAllowed)

		return "", "", false
	}

	host, port, err = net.SplitHostPort(request.Host)
	if err != nil || host == "" || port == "" {
		writeStatus(conn, http.StatusBadRequest)

		return "", "", false
	}

	if options.Allow != nil && !options.Allow(host) {
		refuse(conn, host, options)

		return "", "", false
	}

	return host, port, true
}

func refuse(conn net.Conn, host string, options Options) {
	writeStatus(conn, http.StatusForbidden)

	if options.OnRefused != nil {
		options.OnRefused(host)
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

// RefusalLog returns an OnRefused function that writes each refused host to
// the writer once, so that a guest that keeps trying cannot fill the log.
func RefusalLog(w io.Writer) func(host string) {
	var mu sync.Mutex

	seen := map[string]bool{}

	return func(host string) {
		mu.Lock()
		defer mu.Unlock()

		if seen[host] {
			return
		}

		seen[host] = true

		_, _ = fmt.Fprintf(w, "%s refused %q\n", time.Now().UTC().Format(time.RFC3339), host)
	}
}

func writeStatus(conn net.Conn, status int) {
	headers := "Content-Length: 0\r\n"
	if status != http.StatusOK {
		headers += "Connection: close\r\n"
	}

	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n%s\r\n", status, statusText(status), headers)
}

// statusText is the reason phrase. Proxies answer CONNECT with
// "Connection Established", which Go does not know.
func statusText(status int) string {
	if status == http.StatusOK {
		return "Connection Established"
	}

	return http.StatusText(status)
}
