package ide_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/ide"
)

const patience = 5 * time.Second

// fakeIDE is the server of an IDE. It records how the bridge connected and
// what reached it, and sends what the test puts into send.
type fakeIDE struct {
	port     uint16
	header   chan http.Header
	protocol chan string
	got      chan string
	send     chan string
}

func newFakeIDE(t *testing.T) *fakeIDE {
	t.Helper()

	f := &fakeIDE{
		header:   make(chan http.Header, 10),
		protocol: make(chan string, 10),
		got:      make(chan string, 10),
		send:     make(chan string, 10),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.header <- r.Header.Clone()

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"mcp"}})
		if err != nil {
			return
		}

		defer func() { _ = conn.CloseNow() }()

		f.protocol <- conn.Subprotocol()

		go func() {
			for message := range f.send {
				if conn.Write(r.Context(), websocket.MessageText, []byte(message)) != nil {
					return
				}
			}
		}()

		for {
			_, message, err := conn.Read(r.Context())
			if err != nil {
				return
			}

			f.got <- string(message)
		}
	}))
	t.Cleanup(server.Close)

	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)

	n, err := strconv.ParseUint(port, 10, 16)
	require.NoError(t, err)

	f.port = uint16(n)

	return f
}

// connectVM connects to the bridge like Claude Code in the VM does.
func connectVM(t *testing.T, bridge *ide.Bridge, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()

	server := httptest.NewServer(bridge)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), patience)
	t.Cleanup(cancel)

	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{
		Subprotocols: []string{"mcp"},
		HTTPHeader:   http.Header{ide.AuthHeader: {token}},
	})
	if conn != nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
	}

	return conn, response, err
}

func linked(t *testing.T) (*fakeIDE, *websocket.Conn) {
	t.Helper()

	f := newFakeIDE(t)

	vm, _, err := connectVM(t, &ide.Bridge{IDE: ide.IDE{Port: f.port, Token: "token-of-the-ide"}, Token: "token-of-the-vm", Project: t.TempDir()}, "token-of-the-vm")
	require.NoError(t, err)

	return f, vm
}

func send(t *testing.T, conn *websocket.Conn, message string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(message)))
}

func receive(t *testing.T, conn *websocket.Conn) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()

	_, message, err := conn.Read(ctx)
	require.NoError(t, err)

	return string(message)
}

func arrived(t *testing.T, f *fakeIDE) string {
	t.Helper()

	select {
	case message := <-f.got:
		return message
	case <-time.After(patience):
		t.Fatal("nothing reached the IDE")

		return ""
	}
}

func TestBridgeRefusesAVMWithoutItsToken(t *testing.T) {
	for name, token := range map[string]string{"no token": "", "the token of the IDE": "token-of-the-ide"} {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newFakeIDE(t)
			bridge := &ide.Bridge{IDE: ide.IDE{Port: f.port, Token: "token-of-the-ide"}, Token: "token-of-the-vm"}

			// act
			_, response, err := connectVM(t, bridge, token)

			// assert
			require.Error(t, err)
			require.NotNil(t, response)
			assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
			assert.Empty(t, f.header)
		})
	}
}

func TestBridgeConnectsToTheIDEWithTheTokenOfTheIDE(t *testing.T) {
	// act
	f, _ := linked(t)

	// assert
	header := <-f.header
	assert.Equal(t, "token-of-the-ide", header.Get(ide.AuthHeader))
	assert.Equal(t, "mcp", <-f.protocol)
}

func TestBridgeAnswers502WhenTheIDEDoesNotAnswer(t *testing.T) {
	// arrange
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := uint16(listener.Addr().(*net.TCPAddr).Port) //nolint:gosec // a port of the loopback
	require.NoError(t, listener.Close())

	// act
	_, response, err := connectVM(t, &ide.Bridge{IDE: ide.IDE{Port: port}, Token: "token-of-the-vm"}, "token-of-the-vm")

	// assert
	require.Error(t, err)
	require.NotNil(t, response)
	assert.Equal(t, http.StatusBadGateway, response.StatusCode)
}

func TestBridgePassesTheHandshakeBothWays(t *testing.T) {
	// arrange
	f, vm := linked(t)
	// act
	send(t, vm, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`)
	got := arrived(t, f)
	f.send <- `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25"}}`

	// assert: the IDE sees an ID of the bridge, the VM its own
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, got)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-11-25"}}`, receive(t, vm))

	for _, notification := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"ide_connected","params":{"pid":10870}}`,
	} {
		send(t, vm, notification)
		assert.JSONEq(t, notification, arrived(t, f))
	}
}

func TestBridgeAnswersARequestItHoldsBackWithAnError(t *testing.T) {
	// arrange
	f, vm := linked(t)

	// act
	send(t, vm, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"executeCode","arguments":{"code":"import os"}}}`)

	// assert
	answer := receive(t, vm)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"aibox does not pass this on to the IDE"}}`, answer)
	assert.Empty(t, f.got)
}

func TestBridgeHoldsBackWhatItDoesNotKnow(t *testing.T) {
	// arrange
	f, vm := linked(t)

	// act
	for _, message := range []string{
		`{"jsonrpc":"2.0","method":"some/notification"}`,
		`{"jsonrpc":"2.0","id":3,"result":{}}`,
		`{"jsonrpc":"2.0","method":7}`,
	} {
		send(t, vm, message)
	}

	send(t, vm, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, arrived(t, f))
}

// JavaScript reads keys exactly and takes the last of two equal ones,
// where Go would match them in any case. The bridge must see the method
// the IDE sees.
func TestBridgeReadsKeysTheWayTheIDEDoes(t *testing.T) {
	tests := map[string]struct {
		message, want string
	}{
		"a key in another case": {
			`{"jsonrpc":"2.0","id":7,"method":"tools/call","METHOD":"initialize","params":{"name":"executeCode"}}`,
			`{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"aibox does not pass this on to the IDE"}}`,
		},
		"two equal keys": {
			`{"jsonrpc":"2.0","id":7,"method":"initialize","method":"tools/call","params":{"name":"executeCode"}}`,
			`{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"aibox does not pass this on to the IDE"}}`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			f, vm := linked(t)

			// act
			send(t, vm, test.message)

			// assert
			assert.JSONEq(t, test.want, receive(t, vm))
			assert.Empty(t, f.got)
		})
	}
}

func TestBridgePassesOnItsOwnEncodingOfAMessage(t *testing.T) {
	// arrange
	f, vm := linked(t)

	// act
	send(t, vm, `{"jsonrpc":"2.0","method":"tools/call","method":"notifications/initialized","Method":"x"}`)

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, arrived(t, f))
}

func TestBridgeEndsTheLinkOnAMessageThatIsNoJSONObject(t *testing.T) {
	for _, message := range []string{`not json`, `[{"jsonrpc":"2.0","id":4,"method":"initialize"}]`, `null`} {
		t.Run(message, func(t *testing.T) {
			// arrange
			f, vm := linked(t)

			// act
			send(t, vm, message)

			// assert
			ctx, cancel := context.WithTimeout(context.Background(), patience)
			defer cancel()

			_, _, err := vm.Read(ctx)
			require.Error(t, err)
			assert.Empty(t, f.got)
		})
	}
}

func TestBridgePassesTheToolsItAllowsAndOnlyAnswersToRequestsOfTheVM(t *testing.T) {
	// arrange
	f, vm := linked(t)
	send(t, vm, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	arrived(t, f)

	// act
	f.send <- `{"jsonrpc":"2.0","method":"some/notification","params":{}}`
	f.send <- `{"jsonrpc":"2.0","id":9,"method":"elicitation/create","params":{}}`
	f.send <- `{"jsonrpc":"2.0","id":2,"result":{}}`
	f.send <- `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"executeCode"},{"name":"openDiff"},{"name":"getDiagnostics"}]}}`

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"openDiff"},{"name":"getDiagnostics"}]}}`, receive(t, vm))
}

func TestBridgeKeepsOneLinkAndANewOneReplacesTheOld(t *testing.T) {
	// arrange
	f := newFakeIDE(t)
	bridge := &ide.Bridge{IDE: ide.IDE{Port: f.port, Token: "token-of-the-ide"}, Token: "token-of-the-vm", Project: t.TempDir()}
	old, _, err := connectVM(t, bridge, "token-of-the-vm")
	require.NoError(t, err)

	// act
	_, _, err = connectVM(t, bridge, "token-of-the-vm")
	require.NoError(t, err)

	// assert
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()

	_, _, err = old.Read(ctx)
	assert.Error(t, err)
}

func TestBridgeAnswersAPingOfTheIDE(t *testing.T) {
	// arrange
	f, _ := linked(t)

	// act
	f.send <- `{"jsonrpc":"2.0","id":7,"method":"ping"}`

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{}}`, arrived(t, f))
}
