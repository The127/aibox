//go:build darwin && cgo

package seatbelt_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/the127/aibox/internal/seatbelt"
)

// probeEnv makes the test binary a probe: it confines itself to the port in
// the variable, tries what the arguments name and prints what happened.
const probeEnv = "AIBOX_SEATBELT_PROBE"

// readEnv is a folder the probe may still read.
const readEnv = "AIBOX_SEATBELT_READ"

func TestMain(m *testing.M) {
	if port := os.Getenv(probeEnv); port != "" {
		os.Exit(probe(port, os.Args[1:]))
	}

	os.Exit(m.Run())
}

func probe(port string, tries []string) int {
	p, err := strconv.Atoi(port)
	if err != nil {
		return 2
	}

	// a listener from before the sandbox, as aibox opens the ports of the
	// VM before it confines itself
	var opened net.Listener

	if slices.Contains(tries, "accept") {
		if opened, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return 1
		}
	}

	var read []string
	if dir := os.Getenv(readEnv); dir != "" {
		read = append(read, dir)
	}

	if err := seatbelt.Apply([]uint16{uint16(p)}, read); err != nil { //nolint:gosec // a port of the test
		fmt.Println("apply:", err)

		return 1
	}

	// the test connects only once the sandbox is on
	if opened != nil {
		fmt.Println("listening", opened.Addr())
	}

	for _, try := range tries {
		what, arg, _ := strings.Cut(try, "=")

		var err error

		switch what {
		case "read", "readout":
			_, err = os.ReadFile(arg) //nolint:gosec // a file of the test
		case "list":
			_, err = os.ReadDir(arg)
		case "write":
			err = os.WriteFile(arg, []byte("x"), 0o600) //nolint:gosec // a file of the test
		case "connect":
			var conn net.Conn

			conn, err = net.Dial("tcp", arg) //nolint:gosec // a listener of the test
			if err == nil {
				_ = conn.Close()
			}
		case "exec":
			err = exec.Command(arg).Run() //nolint:gosec // a program of the test
		case "procargs":
			err = procargs(arg)
		case "kill":
			err = signal(arg)
		case "lookup":
			_, err = net.LookupHost(arg) //nolint:gosec // a name of the test
		case "listen":
			var listener net.Listener

			listener, err = net.Listen("tcp", "127.0.0.1:0")
			if err == nil {
				_ = listener.Close()
			}
		case "accept":
			err = acceptOne(opened)
		}

		fmt.Printf("%s %s\n", what, outcome(err))
	}

	return 0
}

// acceptOne accepts a connection and reads what it sends.
func acceptOne(listener net.Listener) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	_, err = io.ReadAll(conn)

	return err
}

// procargs reads the arguments and the environment of the process, which
// macOS hands out through sysctl.
func procargs(pid string) error {
	p, err := strconv.Atoi(pid)
	if err != nil {
		return err
	}

	_, err = unix.SysctlRaw("kern.procargs2", p)

	return err
}

// signal asks whether the process may be signalled, without signalling it.
func signal(pid string) error {
	p, err := strconv.Atoi(pid)
	if err != nil {
		return err
	}

	return syscall.Kill(p, 0)
}

func outcome(err error) string {
	if err == nil {
		return "allowed"
	}

	return "refused"
}

// confined runs the probe confined to the port and returns what it printed.
func confined(t *testing.T, port int, tries ...string) map[string]string {
	t.Helper()

	return confinedReading(t, port, "", tries...)
}

// confinedReading runs the probe as confined does, but lets it read the
// folder.
func confinedReading(t *testing.T, port int, dir string, tries ...string) map[string]string {
	t.Helper()

	cmd := exec.Command(os.Args[0], tries...) //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), probeEnv+"="+strconv.Itoa(port), readEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	results := map[string]string{}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		what, result, _ := strings.Cut(line, " ")
		results[what] = result
	}

	return results
}

func listen(t *testing.T) (net.Listener, int) {
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

			_ = conn.Close()
		}
	}()

	return listener, listener.Addr().(*net.TCPAddr).Port
}

func TestAConfinedProcessConnectsOnlyToTheAllowedPorts(t *testing.T) {
	// arrange
	allowed, port := listen(t)
	other, _ := listen(t)

	// act
	got := confined(t, port, "connect="+allowed.Addr().String())
	refused := confined(t, port, "connect="+other.Addr().String())

	// assert
	assert.Equal(t, "allowed", got["connect"])
	assert.Equal(t, "refused", refused["connect"])
}

func TestAConfinedProcessAcceptsOnAListenerFromBeforeButListensOnNoNewOne(t *testing.T) {
	// arrange
	cmd := exec.Command(os.Args[0], "accept", "listen") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), probeEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	lines := bufio.NewScanner(stdout)
	require.True(t, lines.Scan())
	address, ok := strings.CutPrefix(lines.Text(), "listening ")
	require.True(t, ok, lines.Text())

	// act
	conn, err := net.Dial("tcp", address)
	require.NoError(t, err)
	_, err = io.WriteString(conn, "hello")
	require.NoError(t, err)
	_ = conn.Close()

	var results []string
	for lines.Scan() {
		results = append(results, lines.Text())
	}

	// assert
	require.NoError(t, cmd.Wait())
	assert.Equal(t, []string{"accept allowed", "listen refused"}, results)
}

func TestAConfinedProcessNeitherReadsNorWritesFiles(t *testing.T) {
	// arrange
	_, port := listen(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(file, []byte("secret"), 0o600))

	// act
	got := confined(t, port, "read="+file, "write="+filepath.Join(dir, "new"))

	// assert
	assert.Equal(t, "refused", got["read"])
	assert.Equal(t, "refused", got["write"])
	assert.NoFileExists(t, filepath.Join(dir, "new"))
}

func TestAConfinedProcessReadsTheFoldersItIsGivenButWritesNothingThere(t *testing.T) {
	// arrange
	_, port := listen(t)
	outside := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))

	// t.TempDir is under /var, a link to /private/var, as a project may
	// be under a link
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "justfile"), []byte("x"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "out")))

	// act
	got := confinedReading(t, port, dir,
		"list="+dir,
		"read="+filepath.Join(dir, "justfile"),
		"readout="+filepath.Join(dir, "out"),
		"write="+filepath.Join(dir, "new"))

	// assert
	assert.Equal(t, "allowed", got["list"])
	assert.Equal(t, "allowed", got["read"])
	assert.Equal(t, "refused", got["readout"], "a link must not lead the read out of the folder")
	assert.Equal(t, "refused", got["write"])
	assert.NoFileExists(t, filepath.Join(dir, "new"))
}

func TestAConfinedProcessReadsTheResolverFiles(t *testing.T) {
	// arrange
	_, port := listen(t)

	// act
	hosts := confined(t, port, "read=/etc/hosts")
	resolv := confined(t, port, "read=/etc/resolv.conf")

	// assert
	assert.Equal(t, "allowed", hosts["read"])
	assert.Equal(t, "allowed", resolv["read"], "/etc/resolv.conf links elsewhere, which the sandbox checks")
}

func TestAConfinedProcessStartsNoProgram(t *testing.T) {
	// arrange
	_, port := listen(t)

	// act
	got := confined(t, port, "exec=/usr/bin/true")

	// assert
	assert.Equal(t, "refused", got["exec"])
}

func TestTheProfileAllowsEachPortOfTheAllowList(t *testing.T) {
	// act
	profile := seatbelt.Profile([]uint16{443, 8443}, nil)

	// assert
	assert.Contains(t, profile, `(remote tcp "*:443")`)
	assert.Contains(t, profile, `(remote tcp "*:8443")`)
	assert.True(t, strings.HasPrefix(profile, "(version 1)\n(deny default)"), profile)
}

func TestAConfinedProcessReadsNotTheArgumentsAndEnvironmentOfAnother(t *testing.T) {
	// arrange
	_, port := listen(t)
	parent := strconv.Itoa(os.Getpid())

	// act
	got := confined(t, port, "procargs="+parent)

	// assert
	assert.Equal(t, "refused", got["procargs"])
}

func TestAConfinedProcessSignalsNoOtherProcess(t *testing.T) {
	// arrange
	_, port := listen(t)
	parent := strconv.Itoa(os.Getpid())

	// act
	got := confined(t, port, "kill="+parent)

	// assert
	assert.Equal(t, "refused", got["kill"])
}

func TestAConfinedProcessStillResolvesNames(t *testing.T) {
	// arrange
	_, port := listen(t)

	// act
	got := confined(t, port, "lookup=localhost")

	// assert
	assert.Equal(t, "allowed", got["lookup"])
}
