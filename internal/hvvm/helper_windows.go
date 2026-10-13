package hvvm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strconv"

	"github.com/Microsoft/go-winio/pkg/guid"

	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/hvchild"
	"github.com/the127/aibox/internal/hvsock"
	"github.com/the127/aibox/internal/proxy"
)

// Serve is the helper: it takes the config aibox sends on the pipe under
// the first of the handles of args, serves the proxy of the VM, says so
// and what the proxy does on the pipe under the second, and ends when
// aibox closes the pipes.
func Serve(args []string) error {
	return serve(args, hvsock.Listen)
}

// serve is Serve with the port of the VM listened on with listen.
func serve(args []string, listen func(vm guid.GUID, port uint32) (net.Listener, error)) error {
	if len(args) != 2 {
		return fmt.Errorf("want the handles of the pipes from and to aibox, not %d arguments", len(args))
	}

	var handles [2]uintptr

	for i, arg := range args {
		handle, err := strconv.ParseUint(arg, 10, 64)
		if err != nil {
			return fmt.Errorf("the handle of a pipe: %w", err)
		}

		handles[i] = uintptr(handle)
	}

	in, out := os.NewFile(handles[0], "from aibox"), os.NewFile(handles[1], "to aibox")

	return ServeOn(in, out, os.Stderr, listen)
}

// ServeOn is Serve on the pipes in and out, with what fails in the proxy
// said on stderr and the port of the VM listened on with listen, which a
// test of aibox that runs the helper in its own process gives.
func ServeOn(in io.Reader, out io.Writer, stderr io.Writer, listen func(vm guid.GUID, port uint32) (net.Listener, error)) error {
	// aibox started it, which it trusts
	conn := hvchild.New(in, out, 0)

	configs := make(chan Config, 1)
	served := make(chan error, 1)

	go func() {
		served <- conn.Serve(func(kind string, body json.RawMessage) {
			var c Config
			if kind == kindConfig && json.Unmarshal(body, &c) == nil {
				select {
				case configs <- c:
				default:
				}
			}
		})
	}()

	var c Config

	select {
	case c = <-configs:
	case err := <-served:
		return err
	}

	stop, err := start(conn, c, stderr, listen)
	if err != nil {
		// aibox says why, rather than both of them
		return conn.Send(kindFailed, fit(err.Error()))
	}

	defer stop()

	if err := conn.Send(kindReady, nil); err != nil {
		return err
	}

	return <-served
}

// start serves the proxy, and returns what stops it.
func start(conn *hvchild.Conn, c Config, stderr io.Writer, listen func(guid.GUID, uint32) (net.Listener, error)) (func(), error) {
	local, err := handOver(c)
	if err != nil {
		return nil, err
	}

	l, err := listen(c.VM, c.ProxyPort)
	if err != nil {
		return nil, err
	}

	report := func(event string) func(string) {
		return func(target string) {
			// a name longer than any of DNS, which the proxy refused, would
			// make a message longer than aibox takes
			if isTarget(target) {
				_ = conn.Send(kindProxy, proxyEvent{Event: event, Target: target})
			}
		}
	}

	allow := config.Hosts(c.AllowList)

	return host.ServeProxy(context.Background(), l, proxy.Options{
		Allow:          allow.Allows,
		Pinned:         allow.Pinned,
		Hint:           config.AllowHint(c.AllowFile),
		Local:          local,
		OnConnected:    report(proxyConnected),
		OnRefused:      report(proxyRefused),
		OnClosed:       report(proxyClosed),
		MaxConnections: c.MaxConnections,
	}, stderr), nil
}

// handOver returns the handlers that hand the requests for the services
// on the loopback of the VM to aibox, which holds their secrets, with the
// token, the port they are for and the host the VM asked for.
func handOver(c Config) (func(host, port string) http.Handler, error) {
	if len(c.Local) == 0 {
		return nil, nil //nolint:nilnil // no services is no handlers, and no error
	}

	services, err := url.Parse("http://" + c.Services)
	if err != nil {
		return nil, fmt.Errorf("the address of the services: %w", err)
	}

	// the requests reach aibox as the VM sent them, and only aibox: the
	// default would ask for gzip where the VM did not, and take a proxy of
	// the environment
	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // the default is a Transport
	transport.DisableCompression = true
	transport.Proxy = nil

	return func(host, port string) http.Handler {
		n, err := strconv.ParseUint(port, 10, 16)
		if host != "localhost" || err != nil || !slices.Contains(c.Local, uint16(n)) {
			return nil
		}

		return &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(services)
				r.Out.Host = r.In.Host

				// what the VM sent under these names is not aibox's
				r.Out.Header.Set(tokenHeader, c.Token)
				r.Out.Header.Set(portHeader, port)
			},
			Transport: transport,
			// a service may stream, such as the events of the IDE
			FlushInterval: -1,
		}
	}, nil
}
