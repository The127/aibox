package applelaunch_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/the127/aibox/internal/link"
	"github.com/the127/aibox/internal/session"
)

// fakeEnv names the folder of the fake container tool. When it is set, the
// test binary is the tool: it records each call there and plays the guest.
const fakeEnv = "AIBOX_FAKE_CONTAINER"

// How the fake guest behaves, from the environment of the run.
const (
	// fakeRun is "session" (the default), "fail" to end before listening,
	// "hang" to come up never, "hold" to take connections and say nothing,
	// as a tool might while the guest does not listen yet, or "garbage" to
	// answer with something else than a guest
	fakeRun = "FAKE_RUN"
	// fakeDrops is how many connections the fake tool takes and closes
	// before the guest listens, as Apple's tool does
	fakeDrops  = "FAKE_DROPS"
	fakeOutput = "FAKE_OUTPUT"
	fakeCode   = "FAKE_CODE"
	// fakeProxy makes the guest ask the proxy for evil.example first
	fakeProxy = "FAKE_PROXY"
)

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeEnv); dir != "" {
		os.Exit(fakeContainer(dir, os.Args[1:]))
	}

	os.Exit(m.Run())
}

func fakeContainer(dir string, args []string) int {
	calls, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the folder of the test
	if err == nil {
		_, _ = fmt.Fprintln(calls, strings.Join(args, " "))
		_ = calls.Close()
	}

	switch {
	case len(args) == 3 && args[0] == "image" && args[1] == "inspect":
		if args[2] != "aibox:test" {
			fmt.Println("image not found")

			return 1
		}

		return 0
	case len(args) == 3 && args[0] == "volume" && args[1] == "inspect":
		if _, err := os.Stat(filepath.Join(dir, "volume-"+args[2])); err != nil { //nolint:gosec // a name the test chose, in its folder
			return 1
		}

		return 0
	case len(args) == 5 && args[0] == "volume" && args[1] == "create":
		_ = os.WriteFile(filepath.Join(dir, "volume-"+args[4]), []byte(args[3]), 0o600) //nolint:gosec // a name the test chose, in its folder

		return 0
	case len(args) == 2 && args[0] == "kill":
		_ = os.WriteFile(filepath.Join(dir, "killed"), nil, 0o600) //nolint:gosec // the folder of the test

		return 0
	case len(args) > 0 && args[0] == "run":
		return fakeVM(dir, args)
	}

	fmt.Println("the fake does not know", args)

	return 2
}

// fakeVM plays the tool and the guest of `container run`.
func fakeVM(dir string, args []string) int {
	socket := ""

	for i, arg := range args {
		if arg == "--publish-socket" {
			socket, _, _ = strings.Cut(args[i+1], ":")
		}
	}

	fmt.Println("booting the fake VM")

	switch os.Getenv(fakeRun) {
	case "fail":
		fmt.Println("aibox: mount proc on /proc: operation not permitted")

		return 1
	case "hang":
		for {
			if _, err := os.Stat(filepath.Join(dir, "killed")); err == nil { //nolint:gosec // the folder of the test
				return 137
			}

			time.Sleep(10 * time.Millisecond)
		}
	}

	listener, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Println("listen:", err)

		return 1
	}

	switch os.Getenv(fakeRun) {
	case "hold", "garbage":
		return holdUntilKilled(dir, listener, os.Getenv(fakeRun) == "garbage")
	}

	drops, _ := strconv.Atoi(os.Getenv(fakeDrops))
	for range drops {
		conn, err := listener.Accept()
		if err != nil {
			return 1
		}

		_ = conn.Close()
	}

	conn, err := listener.Accept()
	if err != nil {
		return 1
	}

	guest, err := link.Guest(conn)
	if err != nil {
		return 1
	}

	if os.Getenv(fakeProxy) != "" {
		askProxy(dir, guest)
	}

	terminal, err := guest.DialTerminal()
	if err != nil {
		return 1
	}

	code, _ := strconv.Atoi(os.Getenv(fakeCode))
	_ = session.Serve(terminal, func(session.Request) (session.Process, error) {
		return &process{output: strings.NewReader(os.Getenv(fakeOutput)), code: code}, nil
	})

	// the init ends the VM with a restart, which the tool reports as a signal
	return 129
}

// holdUntilKilled takes every connection and keeps it open without a word,
// or with an answer no guest gives, until the VM is killed, which closes
// them.
func holdUntilKilled(dir string, listener net.Listener, garbage bool) int {
	var (
		mu   sync.Mutex
		held []net.Conn
	)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			if garbage {
				_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\n\r\n")
			}

			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()

	for {
		if _, err := os.Stat(filepath.Join(dir, "killed")); err == nil { //nolint:gosec // the folder of the test
			mu.Lock()
			defer mu.Unlock()

			for _, conn := range held {
				_ = conn.Close()
			}

			return 137
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// askProxy asks the proxy of the host for evil.example and keeps the answer.
func askProxy(dir string, guest *link.GuestEnd) {
	conn, err := guest.DialProxy()
	if err != nil {
		return
	}

	defer func() { _ = conn.Close() }()

	_, _ = io.WriteString(conn, "CONNECT evil.example:443 HTTP/1.1\r\nHost: evil.example:443\r\n\r\n")

	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return
	}

	_ = response.Body.Close()
	_ = os.WriteFile(filepath.Join(dir, "proxy"), []byte(response.Status), 0o600) //nolint:gosec // the folder of the test
}

// process is the command in the fake guest.
type process struct {
	output io.Reader
	code   int
}

func (p *process) Read(b []byte) (int, error)  { return p.output.Read(b) }
func (p *process) Write(b []byte) (int, error) { return len(b), nil }
func (p *process) Resize(session.Size) error   { return nil }
func (p *process) Close() error                { return nil }
func (p *process) Wait() (int, error)          { return p.code, nil }
