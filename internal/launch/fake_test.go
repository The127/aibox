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
	"github.com/the127/aibox/internal/vm"
	"github.com/the127/aibox/internal/vsockns"
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
	qemu             string
	virtiofsd        string
	records          string
	proxyListener    chan net.Listener
	terminalListener chan net.Listener
	stdin            *os.File
	stdout           io.Writer
	stderr           io.Writer
	env              []string
	consoleLog       string
}

func fakes(t *testing.T) *fakeProcesses {
	t.Helper()

	dir := t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)

	f := &fakeProcesses{
		qemu:             filepath.Join(dir, "qemu"),
		virtiofsd:        filepath.Join(dir, "virtiofsd"),
		records:          filepath.Join(dir, "records"),
		consoleLog:       filepath.Join(dir, "console.log"),
		proxyListener:    make(chan net.Listener, 1),
		terminalListener: make(chan net.Listener, 1),
	}

	require.NoError(t, os.Symlink(self, f.qemu))
	require.NoError(t, os.Symlink(self, f.virtiofsd))
	require.NoError(t, os.Mkdir(f.records, 0o700))
	t.Setenv("AIBOX_FAKE_RECORDS", f.records)

	return f
}

// options use TCP listeners in place of vsock and /dev/null in place of the
// KVM and vhost devices, so that the tests run on a machine without them.
func (f *fakeProcesses) options() launch.Options {
	options := launch.Options{
		QEMU:          f.qemu,
		Virtiofsd:     f.virtiofsd,
		Stdin:         f.stdin,
		Stdout:        io.Discard,
		Stderr:        f.stderr,
		SocketTimeout: 5 * time.Second,
		OpenVsock:     f.openTCP,
		Env:           f.env,
		KVMDevice:     os.DevNull,
		ConsoleLog:    f.consoleLog,
	}

	if f.stdout != nil {
		options.Stdout = f.stdout
	}

	return options
}

// openTCP stands in for the vsock namespace: two TCP listeners that the
// tests reach as the VM would, and /dev/null as the vhost device.
func (f *fakeProcesses) openTCP() (*vsockns.Vsock, error) {
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	terminal, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	vhost, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}

	f.proxyListener <- proxy
	f.terminalListener <- terminal

	return &vsockns.Vsock{
		Vhost:        vhost,
		Proxy:        &fromGuest{Listener: proxy},
		Terminal:     &fromGuest{Listener: terminal},
		ProxyPort:    uint32(proxy.Addr().(*net.TCPAddr).Port),    //nolint:gosec // a TCP port fits
		TerminalPort: uint32(terminal.Addr().(*net.TCPAddr).Port), //nolint:gosec // a TCP port fits
	}, nil
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

func (guestConn) RemoteAddr() net.Addr { return &vsock.Addr{ContextID: vm.GuestCID, Port: 1} }

// listener is the proxy listener Run opened, or the test fails.
func (f *fakeProcesses) listener(t *testing.T) net.Listener {
	t.Helper()

	return await(t, f.proxyListener, "the proxy")
}

// terminal is the terminal listener Run opened, or the test fails.
func (f *fakeProcesses) terminal(t *testing.T) net.Listener {
	t.Helper()

	return await(t, f.terminalListener, "the terminal")
}

func await(t *testing.T, listeners <-chan net.Listener, what string) net.Listener {
	t.Helper()

	select {
	case listener := <-listeners:
		return listener
	case <-time.After(5 * time.Second):
		t.Fatalf("OpenVsock was not called for %s", what)

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

// fakeQEMU records the descriptors it was given, what kind of file each is,
// and prints a line on the console.
func fakeQEMU(args []string) int {
	var fds, kinds []string

	for _, number := range descriptors(args) {
		fds = append(fds, strconv.Itoa(number))
		kinds = append(kinds, kindOf(number))
	}

	if console := consoleDescriptor(args); console != 0 {
		file := os.NewFile(uintptr(console), "console")
		_, _ = file.WriteString("hello from the console\n")
		_ = file.Close()
	}

	record("qemu", args)
	record("qemu-fds", fds)
	record("qemu-fd-kinds", kinds)

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

// descriptors are the numbers of the files the arguments name, in the order
// of the arguments: fd= and vhostfd= parts of options, and the kernel.
func descriptors(args []string) []int {
	var numbers []int

	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-kernel":
			if number, err := strconv.Atoi(strings.TrimPrefix(args[i+1], "/dev/fd/")); err == nil {
				numbers = append(numbers, number)
			}
		case "-chardev", "-add-fd", "-device":
			for _, part := range strings.Split(args[i+1], ",") {
				value, ok := strings.CutPrefix(part, "fd=")
				if !ok {
					value, ok = strings.CutPrefix(part, "vhostfd=")
				}

				if number, err := strconv.Atoi(value); ok && err == nil {
					numbers = append(numbers, number)
				}
			}
		}
	}

	return numbers
}

// consoleDescriptor is the number of the console socket, or 0.
func consoleDescriptor(args []string) int {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-chardev" && strings.HasPrefix(args[i+1], "socket,id=console,fd=") {
			number, _ := strconv.Atoi(strings.TrimPrefix(args[i+1], "socket,id=console,fd="))

			return number
		}
	}

	return 0
}

// kindOf tells what kind of file the descriptor is, or "missing".
func kindOf(number int) string {
	info, err := os.NewFile(uintptr(number), "").Stat()
	if err != nil {
		return "missing"
	}

	switch {
	case info.Mode().IsRegular():
		return "file"
	case info.Mode()&os.ModeSocket != 0:
		return "socket"
	case info.Mode()&os.ModeCharDevice != 0:
		return "device"
	default:
		return "other"
	}
}
