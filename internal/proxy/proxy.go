// Package proxy forwards connections to the internet with HTTP CONNECT.
package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()

	var tunnels sync.WaitGroup
	defer tunnels.Wait()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			cancel()

			return fmt.Errorf("accept a connection: %w", err)
		}

		tunnels.Add(1)

		go func() {
			defer tunnels.Done()

			tunnel(ctx, conn, options)
		}()
	}
}

// tunnel answers one CONNECT request and then copies bytes both ways.
func tunnel(ctx context.Context, conn net.Conn, options Options) {
	defer func() { _ = conn.Close() }()

	// ending the context ends the handshake and the tunnel alike
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

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

	stopUpstream := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stopUpstream()

	writeStatus(conn, http.StatusOK)

	var directions sync.WaitGroup

	directions.Add(2)

	// the reader holds what the client sent right after its request
	go pipe(&directions, upstream, reader)
	go pipe(&directions, conn, upstream)

	directions.Wait()
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

// pipe copies until the source ends, then tells the destination that no
// more bytes come. A connection without a half close is closed instead.
func pipe(directions *sync.WaitGroup, dst net.Conn, src io.Reader) {
	defer directions.Done()

	_, _ = io.Copy(dst, src)

	if closer, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()

		return
	}

	_ = dst.Close()
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
