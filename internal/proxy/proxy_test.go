package proxy_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/proxy"
)

const timeout = 5 * time.Second

// start runs a proxy and returns its address and a function that stops it
// and returns the error of Serve.
func start(t *testing.T, options proxy.Options) (*url.URL, func() error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- proxy.Serve(ctx, listener, options) }()

	stop := func() error {
		cancel()

		select {
		case err := <-done:
			return err
		case <-time.After(timeout):
			t.Fatal("Serve did not return")

			return nil
		}
	}

	return &url.URL{Scheme: "http", Host: listener.Addr().String()}, stop
}

// serve runs a proxy for the whole test.
func serve(t *testing.T, options proxy.Options) *url.URL {
	t.Helper()

	address, stop := start(t, options)
	t.Cleanup(func() { assert.NoError(t, stop()) })

	return address
}

// dial connects to the proxy with a deadline, so that a proxy that does not
// answer fails the test instead of hanging it.
func dial(t *testing.T, address *url.URL) net.Conn {
	t.Helper()

	conn, err := net.Dial("tcp", address.Host)
	require.NoError(t, err)
	require.NoError(t, conn.SetDeadline(time.Now().Add(timeout)))
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// connect sends a CONNECT request for the target and returns the status
// line and the connection, which is the tunnel after a 200.
func connect(t *testing.T, address *url.URL, target string) (string, net.Conn) {
	t.Helper()

	return request(t, address, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
}

func request(t *testing.T, address *url.URL, raw string) (string, net.Conn) {
	t.Helper()

	conn := dial(t, address)
	_, err := io.WriteString(conn, raw)
	require.NoError(t, err)

	reader := bufio.NewReader(conn)

	status, err := reader.ReadString('\n')
	require.NoError(t, err)

	// the headers of the response, up to the empty line
	for line := ""; line != "\r\n"; {
		line, err = reader.ReadString('\n')
		require.NoError(t, err)
	}

	return strings.TrimSpace(status), conn
}

// echoAfterEOF is a server that answers with everything it read, once the
// client has finished sending.
func echoAfterEOF(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				received, _ := io.ReadAll(conn)
				_, _ = conn.Write(received)
			}()
		}
	}()

	return listener.Addr().String()
}

func TestServeTunnelsHTTPS(t *testing.T) {
	// arrange
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello from the target")
	}))
	t.Cleanup(target.Close)

	client := &http.Client{Timeout: timeout, Transport: &http.Transport{
		Proxy:           http.ProxyURL(serve(t, proxy.Options{})),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the test server has a self-signed certificate
	}}

	// act
	response, err := client.Get(target.URL)

	// assert
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "hello from the target", string(body))
}

func TestServeDeliversTheReplyAfterTheClientStopsSending(t *testing.T) {
	// arrange
	address := serve(t, proxy.Options{})
	status, tunnel := connect(t, address, echoAfterEOF(t))
	require.Equal(t, "HTTP/1.1 200 Connection Established", status)

	// act
	_, err := io.WriteString(tunnel, "sent through the tunnel")
	require.NoError(t, err)
	require.NoError(t, tunnel.(*net.TCPConn).CloseWrite())

	reply, err := io.ReadAll(tunnel)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "sent through the tunnel", string(reply))
}

func TestServeRefusesAHostThatIsNotAllowed(t *testing.T) {
	// arrange
	var mu sync.Mutex
	var asked string
	address := serve(t, proxy.Options{Allow: func(host string) bool {
		mu.Lock()
		defer mu.Unlock()

		asked = host

		return false
	}})

	// act
	status, _ := connect(t, address, "example.com:443")

	// assert
	assert.Equal(t, "HTTP/1.1 403 Forbidden", status)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "example.com", asked)
}

func TestServeReportsARefusedHost(t *testing.T) {
	// arrange
	refused := make(chan string, 1)
	address := serve(t, proxy.Options{
		Allow:     func(string) bool { return false },
		OnRefused: func(host string) { refused <- host },
	})

	// act
	status, _ := connect(t, address, "example.com:443")

	// assert
	assert.Equal(t, "HTTP/1.1 403 Forbidden", status)

	select {
	case host := <-refused:
		assert.Equal(t, "example.com", host)
	case <-time.After(timeout):
		t.Fatal("OnRefused was not called")
	}
}

func TestRefusalLogWritesEachHostOnce(t *testing.T) {
	// arrange
	var log bytes.Buffer
	refused := proxy.RefusalLog(&log)

	// act
	refused("evil.example")
	refused("evil.example")
	refused("other.example")

	// assert
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	require.Len(t, lines, 2)
	assert.Regexp(t, `^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ refused "evil.example"$`, lines[0])
	assert.Contains(t, lines[1], `refused "other.example"`)
}

// resolveTo is a resolver that answers every name with the addresses.
func resolveTo(addresses ...string) func(context.Context, string) ([]net.IP, error) {
	return func(context.Context, string) ([]net.IP, error) {
		var ips []net.IP
		for _, address := range addresses {
			ip := net.ParseIP(address)
			if ip == nil {
				panic("not an address: " + address)
			}

			ips = append(ips, ip)
		}

		return ips, nil
	}
}

func TestServeRefusesANameThatResolvesToTheHostItself(t *testing.T) {
	// arrange
	refused := make(chan string, 1)
	address := serve(t, proxy.Options{
		Resolve:   resolveTo("127.0.0.1"),
		OnRefused: func(host string) { refused <- host },
	})

	// act
	status, _ := connect(t, address, "evil.example:443")

	// assert
	assert.Equal(t, "HTTP/1.1 403 Forbidden", status)

	select {
	case host := <-refused:
		assert.Equal(t, "evil.example", host)
	case <-time.After(timeout):
		t.Fatal("OnRefused was not called")
	}
}

func TestServeRefusesANameThatResolvesToThePrivateNetwork(t *testing.T) {
	// arrange
	address := serve(t, proxy.Options{Resolve: resolveTo("10.0.0.5", "192.168.1.1")})

	// act
	status, _ := connect(t, address, "printer.example:443")

	// assert
	assert.Equal(t, "HTTP/1.1 403 Forbidden", status)
}

func TestServeDialsAListedAddressEvenOnTheHostItself(t *testing.T) {
	// arrange
	target := echoAfterEOF(t)
	host, _, err := net.SplitHostPort(target)
	require.NoError(t, err)

	address := serve(t, proxy.Options{Allow: func(h string) bool { return h == host }})

	// act
	status, _ := connect(t, address, target)

	// assert
	assert.Equal(t, "HTTP/1.1 200 Connection Established", status)
}

func TestServeReportsANameItCannotResolve(t *testing.T) {
	// arrange
	address := serve(t, proxy.Options{Resolve: func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("no such host")
	}})

	// act
	status, _ := connect(t, address, "nowhere.example:443")

	// assert
	assert.Equal(t, "HTTP/1.1 502 Bad Gateway", status)
}

func TestServeRefusesOtherMethods(t *testing.T) {
	// arrange
	address := serve(t, proxy.Options{})

	// act
	status, _ := request(t, address, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")

	// assert
	assert.Equal(t, "HTTP/1.1 405 Method Not Allowed", status)
}

func TestServeRefusesATargetWithoutAHost(t *testing.T) {
	// arrange
	address := serve(t, proxy.Options{})

	for _, target := range []string{":443", "example.com", "example.com:"} {
		t.Run(target, func(t *testing.T) {
			// act
			status, _ := connect(t, address, target)

			// assert
			assert.Equal(t, "HTTP/1.1 400 Bad Request", status)
		})
	}
}

func TestServeReportsATargetItCannotReach(t *testing.T) {
	// arrange
	address := serve(t, proxy.Options{})
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	// act
	status, _ := connect(t, address, closed.Addr().String())

	// assert
	assert.Equal(t, "HTTP/1.1 502 Bad Gateway", status)
}

func TestServeStopsWithAnIdleConnectionOpen(t *testing.T) {
	// arrange
	address, stop := start(t, proxy.Options{})
	idle := dial(t, address)

	// act
	err := stop()

	// assert
	require.NoError(t, err)

	_, err = idle.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

func TestServeStopsWithATunnelOpen(t *testing.T) {
	// arrange
	address, stop := start(t, proxy.Options{})
	status, tunnel := connect(t, address, echoAfterEOF(t))
	require.Equal(t, "HTTP/1.1 200 Connection Established", status)

	// act
	err := stop()

	// assert
	require.NoError(t, err)

	_, err = tunnel.Read(make([]byte, 1))
	assert.Error(t, err)
}
