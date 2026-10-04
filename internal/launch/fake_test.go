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

	"golang.org/x/sys/unix"

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
	case "bwrap":
		// the fake returns only when it could not run the command
		fakeBwrap(os.Args[1:])
		os.Exit(1)
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
	bwrap            string
	virtiofsd        string
	records          string
	proxyListener    chan net.Listener
	terminalListener chan net.Listener
	stdin            *os.File
	stdout           io.Writer
	stderr           io.Writer
	env              []string
	consoleLog       string
	libraries        string
	firmware         string
	ports            []uint16
	confinedWith     []uint16
	confineCalls     int
	failConfine      error
}

func fakes(t *testing.T) *fakeProcesses {
	t.Helper()

	dir := t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)

	f := &fakeProcesses{
		qemu:             filepath.Join(dir, "qemu"),
		bwrap:            filepath.Join(dir, "bwrap"),
		virtiofsd:        filepath.Join(dir, "virtiofsd"),
		records:          filepath.Join(dir, "records"),
		consoleLog:       filepath.Join(dir, "console.log"),
		libraries:        filepath.Join(dir, "lib64"),
		firmware:         filepath.Join(dir, "qboot.rom"),
		proxyListener:    make(chan net.Listener, 1),
		terminalListener: make(chan net.Listener, 1),
	}

	require.NoError(t, os.Symlink(self, f.qemu))
	require.NoError(t, os.Symlink(self, f.bwrap))
	require.NoError(t, os.Symlink(self, f.virtiofsd))
	require.NoError(t, os.Mkdir(f.records, 0o700))
	require.NoError(t, os.Mkdir(f.libraries, 0o700))
	require.NoError(t, os.WriteFile(f.firmware, nil, 0o600))
	t.Setenv("AIBOX_FAKE_RECORDS", f.records)

	return f
}

// options use TCP listeners in place of vsock and /dev/null in place of the
// KVM and vhost devices, so that the tests run on a machine without them.
func (f *fakeProcesses) options() launch.Options {
	options := launch.Options{
		QEMU:          f.qemu,
		Bubblewrap:    f.bwrap,
		Virtiofsd:     f.virtiofsd,
		Stdin:         f.stdin,
		Stdout:        io.Discard,
		Stderr:        f.stderr,
		SocketTimeout: 5 * time.Second,
		OpenVsock:     f.openTCP,
		Confine:       f.confine,
		Ports:         f.ports,
		Env:           f.env,
		KVMDevice:     os.DevNull,
		ConsoleLog:    f.consoleLog,
		Libraries:     f.libraries,
		Firmware:      f.firmware,
		// the real waits are for a VM, the fakes end at once. The session
		// still needs longer than a stop, as in a real run
		StopDelay:       100 * time.Millisecond,
		SessionEndDelay: 300 * time.Millisecond,
	}

	if f.stdout != nil {
		options.Stdout = f.stdout
	}

	return options
}

// confine stands in for the confinement of aibox, which the test process
// could not undo. Run calls it on the test's goroutine. A failure waits
// for the fake QEMU to be up, so that the test sees it stopped.
func (f *fakeProcesses) confine(ports []uint16) error {
	f.confinedWith = ports
	f.confineCalls++

	if f.failConfine != nil {
		for range 500 {
			if _, err := os.Stat(filepath.Join(f.records, "qemu")); err == nil {
				break
			}

			time.Sleep(10 * time.Millisecond)
		}
	}

	return f.failConfine
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

// fakeBwrap records its own arguments and runs the command after "--" in
// its place, as bubblewrap would: the kernel file is read and closed, the
// environment is cleared but for what --setenv gives and the variables
// that steer the fakes, and every other file passes through.
func fakeBwrap(args []string) {
	var env []string

	for _, variable := range os.Environ() {
		if strings.HasPrefix(variable, "AIBOX_FAKE_") {
			env = append(env, variable)
		}
	}

	for i, arg := range args {
		switch {
		case arg == "--setenv" && i+2 < len(args):
			env = append(env, args[i+1]+"="+args[i+2])
		case arg == "--ro-bind-data" && i+1 < len(args):
			number, _ := strconv.Atoi(args[i+1])
			record("bwrap-kernel-kind", []string{kindOf(number)})
			_ = os.NewFile(uintptr(number), "kernel").Close()
		case arg == "--" && i+1 < len(args):
			record("bwrap", args[:i])

			command := args[i+1:]

			err := syscall.Exec(command[0], command, env) //nolint:gosec // the command comes from the test
			fmt.Fprintln(os.Stderr, "fake bwrap:", err)

			return
		}
	}

	fmt.Fprintln(os.Stderr, "fake bwrap: no command")
}

// fakeQEMU records the descriptors it was given, what kind of file each is,
// and prints a line on the console.
func fakeQEMU(args []string) int {
	// the signal may come right after the start
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)

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

	record("qemu-pid", []string{strconv.Itoa(os.Getpid())})
	record("qemu", args)
	record("qemu-program", []string{os.Args[0]})
	record("qemu-environment", os.Environ())
	record("qemu-fds", fds)
	record("qemu-fd-kinds", kinds)

	for i, arg := range args {
		if arg == "-append" && i+1 < len(args) {
			record("qemu-cmdline", strings.Fields(args[i+1]))
		}
	}

	fmt.Println("fake qemu ran")

	if os.Getenv("AIBOX_FAKE_QEMU") == "wait" {
		<-stop
		record("qemu-exited", nil)
	}

	code, _ := strconv.Atoi(os.Getenv("AIBOX_FAKE_QEMU_EXIT"))

	return code
}

// descriptors are the numbers of the files the arguments name, in the order
// of the arguments: the fd= and vhostfd= parts of options.
func descriptors(args []string) []int {
	var numbers []int

	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
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

// accessMode is "ro" or "rw" for the mode a file was opened with.
func accessMode(number int) string {
	flags, err := unix.FcntlInt(uintptr(number), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE == unix.O_RDONLY {
		return "ro"
	}

	return "rw"
}

// kindOf tells what kind of file the descriptor is, or "missing".
func kindOf(number int) string {
	info, err := os.NewFile(uintptr(number), "").Stat()
	if err != nil {
		return "missing"
	}

	switch {
	case info.Mode().IsRegular():
		return "file " + accessMode(number)
	case info.Mode()&os.ModeSocket != 0:
		return "socket"
	case info.Mode()&os.ModeCharDevice != 0:
		return "device"
	default:
		return "other"
	}
}
