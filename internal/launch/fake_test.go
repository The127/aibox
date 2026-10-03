package launch_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mdlayher/vsock"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/launch"
)

// The test binary also stands in for virtiofsd and QEMU. Run starts it
// through links named after them, and TestMain picks the fake by that name.
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "virtiofsd":
		os.Exit(fakeVirtiofsd(os.Args[1:]))
	case "qemu":
		os.Exit(fakeQEMU(os.Args[1:]))
	}

	os.Exit(m.Run())
}

// exitingShare is the share whose fake virtiofsd exits at once when
// AIBOX_FAKE_VIRTIOFSD is "exit".
const exitingShare = "/home/someone/project"

// syncBuffer collects the output of several processes at once.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

// fakeProcesses are links to the test binary named after the programs Run
// starts. The fakes write what they were started with into records.
type fakeProcesses struct {
	qemu          string
	virtiofsd     string
	records       string
	proxyListener chan net.Listener
}

func fakes(t *testing.T) *fakeProcesses {
	t.Helper()

	dir := t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)

	f := &fakeProcesses{
		qemu:          filepath.Join(dir, "qemu"),
		virtiofsd:     filepath.Join(dir, "virtiofsd"),
		records:       filepath.Join(dir, "records"),
		proxyListener: make(chan net.Listener, 1),
	}

	require.NoError(t, os.Symlink(self, f.qemu))
	require.NoError(t, os.Symlink(self, f.virtiofsd))
	require.NoError(t, os.Mkdir(f.records, 0o700))
	t.Setenv("AIBOX_FAKE_RECORDS", f.records)

	return f
}

// options use a TCP listener in place of vsock, so that the tests run on a
// machine without vsock.
func (f *fakeProcesses) options() launch.Options {
	return launch.Options{
		QEMU:          f.qemu,
		Virtiofsd:     f.virtiofsd,
		Stdout:        io.Discard,
		SocketTimeout: 5 * time.Second,
		ListenVsock:   f.listenTCP,
	}
}

func (f *fakeProcesses) listenTCP() (net.Listener, uint32, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}

	f.proxyListener <- listener

	return &fromGuest{Listener: listener}, uint32(listener.Addr().(*net.TCPAddr).Port), nil //nolint:gosec // a TCP port fits
}

// fromGuest gives every connection the vsock address of the test machine, as
// a vsock listener would.
type fromGuest struct {
	net.Listener
}

func (l *fromGuest) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	return guestConn{Conn: conn}, nil
}

type guestConn struct {
	net.Conn
}

func (guestConn) RemoteAddr() net.Addr { return &vsock.Addr{ContextID: guestCID, Port: 1} }

// listener is the proxy listener Run opened, or the test fails.
func (f *fakeProcesses) listener(t *testing.T) net.Listener {
	t.Helper()

	select {
	case listener := <-f.proxyListener:
		return listener
	case <-time.After(5 * time.Second):
		t.Fatal("ListenVsock was not called")

		return nil
	}
}

func (f *fakeProcesses) record(t *testing.T, name string) []string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(f.records, name)) //nolint:gosec // records is a temp folder of the test
	require.NoError(t, err)

	if len(content) == 0 {
		return nil
	}

	return strings.Split(string(content), "\n")
}

func record(name string, lines []string) {
	path := filepath.Join(os.Getenv("AIBOX_FAKE_RECORDS"), name)
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600) //nolint:gosec // the folder comes from the test
}

func fakeVirtiofsd(args []string) int {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	var socket, dir string

	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--socket-path="); ok {
			socket = value
		}

		if value, ok := strings.CutPrefix(arg, "--shared-dir="); ok {
			dir = value
		}
	}

	if os.Getenv("AIBOX_FAKE_VIRTIOFSD") == "exit" && dir == exitingShare {
		fmt.Fprintln(os.Stderr, "fake virtiofsd: cannot open the shared folder")

		return 1
	}

	if os.Getenv("AIBOX_FAKE_VIRTIOFSD") != "no-socket" {
		listener, err := net.Listen("unix", socket)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)

			return 1
		}

		defer func() { _ = listener.Close() }()
	}

	record("virtiofsd-"+filepath.Base(socket), args)
	<-stop
	record("virtiofsd-"+filepath.Base(socket)+"-stopped", nil)

	return 0
}

func fakeQEMU(args []string) int {
	var sockets, missing []string

	for i, arg := range args {
		if arg != "-chardev" || i+1 == len(args) {
			continue
		}

		for _, part := range strings.Split(args[i+1], ",") {
			if path, ok := strings.CutPrefix(part, "path="); ok {
				sockets = append(sockets, path)

				if _, err := os.Stat(path); err != nil { //nolint:gosec // the path comes from the test
					missing = append(missing, path)
				}
			}
		}
	}

	record("qemu", args)
	record("qemu-sockets", sockets)
	record("qemu-missing-sockets", missing)

	for i, arg := range args {
		if arg == "-append" && i+1 < len(args) {
			record("qemu-cmdline", strings.Fields(args[i+1]))
		}
	}

	fmt.Println("fake qemu ran")

	if os.Getenv("AIBOX_FAKE_QEMU") == "wait" {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM)
		<-stop
		record("qemu-exited", nil)
	}

	code, _ := strconv.Atoi(os.Getenv("AIBOX_FAKE_QEMU_EXIT"))

	return code
}
