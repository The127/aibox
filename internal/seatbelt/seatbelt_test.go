//go:build darwin && cgo

package seatbelt_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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

	if err := seatbelt.Apply([]uint16{uint16(p)}); err != nil { //nolint:gosec // a port of the test
		fmt.Println("apply:", err)

		return 1
	}

	for _, try := range tries {
		what, arg, _ := strings.Cut(try, "=")

		var err error

		switch what {
		case "read":
			_, err = os.ReadFile(arg) //nolint:gosec // a file of the test
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
		}

		fmt.Printf("%s %s\n", what, outcome(err))
	}

	return 0
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

	cmd := exec.Command(os.Args[0], tries...) //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), probeEnv+"="+strconv.Itoa(port))
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
	profile := seatbelt.Profile([]uint16{443, 8443})

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
