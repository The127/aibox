package ide

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// AuthHeader is where Claude Code shows the token of a lock file.
const AuthHeader = "X-Claude-Code-Ide-Authorization"

// subprotocol is what Claude Code and the IDEs speak over the WebSocket.
const subprotocol = "mcp"

// maxMessageBytes is the most a message may have. A diff that Claude Code
// shows in the IDE carries the whole new file, so a big source file must
// fit, but the VM must not make aibox hold any amount of memory.
const maxMessageBytes = 64 << 20

// dialTimeout is how long the IDE may take to answer. It runs on the same
// machine, so it answers at once or not at all.
const dialTimeout = 10 * time.Second

// Bridge links Claude Code in the VM to the IDE on the host. The VM shows
// Token, a token of its own, and the bridge shows the IDE the token of the
// IDE, which the VM never sees. Only what the bridge knows to be harmless
// goes through, since the IDE runs with the rights of the person.
type Bridge struct {
	IDE   IDE
	Token string
	// Project is the folder of the project on the host, which is /project
	// in the VM
	Project string
	// Log gets what the bridge held back and why the link ended, or is nil
	Log io.Writer

	// mu guards end, which ends the link that runs. A VM has one link at a
	// time, so that it cannot make aibox hold the memory of many, and a
	// new one replaces the old.
	mu  sync.Mutex
	end context.CancelFunc
}

func (b *Bridge) logf(format string, args ...any) {
	if b.Log != nil {
		_, _ = fmt.Fprintf(b.Log, time.Now().UTC().Format(time.RFC3339)+" ide: "+format+"\n", args...)
	}
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(AuthHeader)), []byte(b.Token)) != 1 {
		http.Error(w, "wrong token for the IDE", http.StatusUnauthorized)

		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	b.mu.Lock()
	if b.end != nil {
		b.end()
	}

	b.end = cancel
	b.mu.Unlock()

	dialCtx, cancelDial := context.WithTimeout(ctx, dialTimeout)
	defer cancelDial()

	ide, _, err := websocket.Dial(dialCtx, "ws://127.0.0.1:"+strconv.Itoa(int(b.IDE.Port)), &websocket.DialOptions{
		Subprotocols: []string{subprotocol},
		HTTPHeader:   http.Header{AuthHeader: {b.IDE.Token}},
	})
	if err != nil {
		b.logf("connect to %s: %v", b.IDE, err)
		http.Error(w, "the IDE does not answer", http.StatusBadGateway)

		return
	}

	defer func() { _ = ide.CloseNow() }()

	vm, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{subprotocol}})
	if err != nil {
		return
	}

	defer func() { _ = vm.CloseNow() }()

	vm.SetReadLimit(maxMessageBytes)
	ide.SetReadLimit(maxMessageBytes)

	filter := newFilter(paths{host: b.Project})
	done := make(chan string, 2)

	b.logf("linked to %s", b.IDE)

	go func() {
		err := pump(ctx, vm, func(message []byte) error {
			forward, reply, err := filter.toIDE(message)
			if err != nil {
				return err
			}

			if forward != nil {
				return ide.Write(ctx, websocket.MessageText, forward)
			}

			b.logf("held back from the IDE: %s", summary(message))

			if reply != nil {
				return vm.Write(ctx, websocket.MessageText, reply)
			}

			return nil
		})
		done <- "the VM: " + err.Error()
	}()

	go func() {
		err := pump(ctx, ide, func(message []byte) error {
			forward, reply := filter.toVM(message)
			if forward != nil {
				return vm.Write(ctx, websocket.MessageText, forward)
			}

			if reply != nil {
				return ide.Write(ctx, websocket.MessageText, reply)
			}

			b.logf("held back from the VM: %s", summary(message))

			return nil
		})
		done <- "the IDE: " + err.Error()
	}()

	b.logf("link ended by %s", <-done)
}

// pump hands each text message of the connection to handle until the
// connection or handle fails, and returns why.
func pump(ctx context.Context, conn *websocket.Conn, handle func(message []byte) error) error {
	for {
		kind, message, err := conn.Read(ctx)
		if err != nil {
			return err
		}

		if kind != websocket.MessageText {
			continue
		}

		if err := handle(message); err != nil {
			return err
		}
	}
}

// maxLoggedName is how much of a name from the VM goes into the log. The
// methods and tools of Claude Code are short, and a longer name only
// fills the log.
const maxLoggedName = 100

// summary names a message for the log by its method and, for a tool call,
// the tool, and leaves out what it carries, which may be the content of
// files.
func summary(data []byte) string {
	m, ok := parse(data)

	switch method, isRequest := m.method(); {
	case !ok:
		return "a message that is no JSON-RPC"
	case method == "tools/call":
		var params message
		_ = json.Unmarshal(m["params"], &params)
		name := params.str("name")

		return "tools/call " + quoted(name)
	case isRequest:
		return quoted(method)
	default:
		return "an answer"
	}
}

// quoted is a name from the VM fit for one line of the log.
func quoted(name string) string {
	if len(name) > maxLoggedName {
		name = name[:maxLoggedName] + "..."
	}

	return strconv.Quote(name)
}

// message is a JSON-RPC message as JavaScript reads it: by the exact key,
// and the last of two equal keys wins. Decoding into a struct would match
// keys in any case, so a key like METHOD could make the bridge see another
// method than the IDE does. What the bridge passes on is its own encoding
// of the message, so the other side reads what the bridge decided on.
type message map[string]json.RawMessage

func parse(data []byte) (message, bool) {
	var m message
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return nil, false
	}

	return m, true
}

// str is the value of the key if it is a string, else empty.
func (m message) str(key string) string {
	var value string
	if raw, ok := m[key]; ok {
		_ = json.Unmarshal(raw, &value)
	}

	return value
}

// method is the method of a request or notification, and whether the
// message has one at all. A method that is no string still counts, so
// that such a message is never taken for an answer.
func (m message) method() (string, bool) {
	_, has := m["method"]
	return m.str("method"), has
}
