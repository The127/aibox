// Package proxy forwards connections to the internet with HTTP CONNECT.
package proxy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
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

	target, ok := handshake(conn, reader, options)
	if !ok {
		return
	}

	dialer := net.Dialer{Timeout: dialTimeout}

	upstream, err := dialer.DialContext(ctx, "tcp", target)
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

// handshake reads the CONNECT request and returns the target to dial. It
// answers the client itself when there is nothing to dial.
func handshake(conn net.Conn, reader *bufio.Reader, options Options) (string, bool) {
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))

	request, err := http.ReadRequest(reader)
	if err != nil {
		return "", false
	}

	_ = conn.SetReadDeadline(time.Time{})

	if request.Method != http.MethodConnect {
		writeStatus(conn, http.StatusMethodNotAllowed)

		return "", false
	}

	host, port, err := net.SplitHostPort(request.Host)
	if err != nil || host == "" || port == "" {
		writeStatus(conn, http.StatusBadRequest)

		return "", false
	}

	if options.Allow != nil && !options.Allow(host) {
		writeStatus(conn, http.StatusForbidden)

		return "", false
	}

	return request.Host, true
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
