package hvvm

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hvchild"
)

// targets keeps what the events of a proxy name.
type targets struct {
	mu      sync.Mutex
	refused []string
	got     chan struct{}
}

func newTargets() *targets { return &targets{got: make(chan struct{}, 16)} }

func (tg *targets) events() Events {
	return Events{OnRefused: func(target string) {
		tg.mu.Lock()
		tg.refused = append(tg.refused, target)
		tg.mu.Unlock()

		tg.got <- struct{}{}
	}}
}

func (tg *targets) all() []string {
	tg.mu.Lock()
	defer tg.mu.Unlock()

	return append([]string(nil), tg.refused...)
}

// loopback listens on the loopback for the port of the VM, and tells
// where.
func loopback(addr chan<- string) func(guid.GUID, uint32) (net.Listener, error) {
	return func(guid.GUID, uint32) (net.Listener, error) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			addr <- l.Addr().String()
		}

		return l, err
	}
}

// served runs the helper over pipes in memory, configured with c, and
// returns why it could not serve, if it could not.
func served(t *testing.T, listen func(guid.GUID, uint32) (net.Listener, error), events Events, c Config) error {
	t.Helper()

	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()

	done := make(chan error, 1)

	go func() {
		done <- ServeOn(toR, fromW, io.Discard, listen)

		_ = fromW.Close()
	}()

	h := Connect(fromR, toW, events, toW)

	t.Cleanup(func() {
		_ = h.Close()

		select {
		case err := <-done:
			assert.NoError(t, err, "the helper")
		case <-time.After(10 * time.Second):
			t.Error("the helper outlived aibox")
		}
	})

	return h.Configure(t.Context(), c)
}

// connect asks the proxy at addr for a tunnel to target, and returns it
// and the status of the answer.
func connect(t *testing.T, addr, target string) (net.Conn, *bufio.Reader, int) {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	require.NoError(t, err)

	r := bufio.NewReader(conn)

	resp, err := http.ReadResponse(r, nil)
	require.NoError(t, err)

	return conn, r, resp.StatusCode
}

func TestTheHelperRefusesATargetOffTheAllowListAndTellsAibox(t *testing.T) {
	t.Parallel()

	// arrange
	addr := make(chan string, 1)
	tg := newTargets()

	err := served(t, loopback(addr), tg.events(), Config{ProxyPort: 1025, AllowList: []string{"example.com:443"}})
	require.NoError(t, err)

	// act
	_, _, status := connect(t, <-addr, "example.org:443")

	// assert
	assert.NotEqual(t, http.StatusOK, status)

	select {
	case <-tg.got:
	case <-time.After(10 * time.Second):
		t.Fatal("aibox was not told")
	}

	assert.Equal(t, []string{"example.org:443"}, tg.all())
}

func TestTheHelperHandsTheRequestsOfTheServicesToAibox(t *testing.T) {
	t.Parallel()

	// arrange: aibox's services, of which the one on port 3129 answers
	// with what it got
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	stop := ServeServices(l, "the token", func(host, port string) http.Handler {
		if host != "localhost" || port != "3129" {
			return nil
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, "%s %s token=%q port=%q", r.Host, r.URL.Path, r.Header.Get(tokenHeader), r.Header.Get(portHeader)) //nolint:gosec // an answer only the test reads
		})
	})
	t.Cleanup(stop)

	addr := make(chan string, 1)

	err = served(t, loopback(addr), Events{}, Config{ProxyPort: 1025, Local: []uint16{3129}, Services: l.Addr().String(), Token: "the token"})
	require.NoError(t, err)

	conn, r, status := connect(t, <-addr, "localhost:3129")
	require.Equal(t, http.StatusOK, status)

	// act: a request of the VM in the tunnel, which names a token of its own
	_, err = fmt.Fprint(conn, "GET /v1/messages HTTP/1.1\r\nHost: localhost:3129\r\nAibox-Token: forged\r\nAibox-Port: 1\r\n\r\n")
	require.NoError(t, err)

	resp, err := http.ReadResponse(r, nil)
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// assert
	assert.Equal(t, `localhost:3129 /v1/messages token="" port=""`, string(body), "answered by aibox, which gets neither header")
}

func TestServeServicesAnswersOnlyTheHelper(t *testing.T) {
	t.Parallel()

	// arrange
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	stop := ServeServices(l, "the token", func(string, string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "secret") })
	})
	t.Cleanup(stop)

	tests := map[string]struct {
		token string
		want  int
	}{
		"no token":         {"", http.StatusForbidden},
		"another token":    {"the token, nearly", http.StatusForbidden},
		"the helper's one": {"the token", http.StatusOK},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+l.Addr().String()+"/", nil)
			require.NoError(t, err)

			if tt.token != "" {
				req.Header.Set(tokenHeader, tt.token)
			}

			// act
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)

			_ = resp.Body.Close()

			// assert
			assert.Equal(t, tt.want, resp.StatusCode)
		})
	}
}

func TestTheHelperSaysWhyItCantServe(t *testing.T) {
	t.Parallel()

	// arrange
	refuse := func(guid.GUID, uint32) (net.Listener, error) {
		return nil, fmt.Errorf("the port is taken%s", "\x1b[2J")
	}

	// act
	err := served(t, refuse, Events{}, Config{ProxyPort: 1025})

	// assert
	require.ErrorContains(t, err, "the port is taken")
	assert.NotContains(t, err.Error(), "\x1b", "without its control characters")
}

func TestAiboxTakesOnlyTargetsThatAreAHostAndPort(t *testing.T) {
	t.Parallel()

	// arrange: a helper that sends events of its own making
	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()

	go func() { _, _ = io.Copy(io.Discard, toR) }()

	tg := newTargets()
	h := Connect(fromR, toW, tg.events(), toW)
	helper := hvchild.New(toR, fromW, 0, fromW)

	// the helper ends first, as aibox reads until it does
	t.Cleanup(func() {
		_ = helper.Close()
		_ = h.Close()
	})

	// act
	for _, target := range []string{"example.com:443", strings.Repeat("a", maxHost+1) + ":443", "example.com", "example.com:99999", "\x1b[2Jexample.com:443", "[::1]:22"} {
		require.NoError(t, helper.Send(kindProxy, proxyEvent{Event: proxyRefused, Target: target}))
	}

	require.NoError(t, helper.Send(kindReady, nil))
	require.NoError(t, h.Configure(t.Context(), Config{}))

	// assert
	assert.Equal(t, []string{"example.com:443", "[::1]:22"}, tg.all())
}

func TestWhyTheHelperCantServeFitsAMessageAiboxReads(t *testing.T) {
	t.Parallel()

	// act
	size, err := hvchild.Size(kindFailed, fit(strings.Repeat("\x00", 1<<20)))

	// assert
	require.NoError(t, err)
	assert.LessOrEqual(t, size, maxFromHelper())
}

func TestTheHelperSaysNothingOfATargetLongerThanAnyAndServesOn(t *testing.T) {
	t.Parallel()

	// arrange
	addr := make(chan string, 1)
	tg := newTargets()

	err := served(t, loopback(addr), tg.events(), Config{ProxyPort: 1025})
	require.NoError(t, err)

	proxyAddr := <-addr

	// act
	_, _, status := connect(t, proxyAddr, strings.Repeat("a", maxHost+1)+".example:443")

	// assert
	assert.NotEqual(t, http.StatusOK, status)

	_, _, _ = connect(t, proxyAddr, "example.org:443")

	select {
	case <-tg.got:
	case <-time.After(10 * time.Second):
		t.Fatal("aibox was not told of the target after it")
	}

	assert.Equal(t, []string{"example.org:443"}, tg.all(), "only the target a host can be")
}

func TestWhatTheHelperSaysReachesAiboxWithoutControlCharacters(t *testing.T) {
	t.Parallel()

	// arrange
	var out strings.Builder

	// act
	copyPrintable(&out, strings.NewReader("aibox: the proxy stopped\n\x1b]52;c;aGk=\x07\x1b[2J\r\xc2\x9bforged\xff\n"))

	// assert
	assert.Equal(t, "aibox: the proxy stopped\n?]52;c;aGk=??[2J??forged?\n", out.String())
}
