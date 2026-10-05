//go:build linux && amd64

package confine_test

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/the127/aibox/internal/confine"
)

// The test binary is also the confined process: started as "probe" with an
// allowed port, another port and a file of its own, it confines its thread
// and then tries one thing after another, reporting each as a line
// "name: ok", "name: EACCES", "name: EPERM" or "name: <error>".
func TestMain(m *testing.M) {
	if len(os.Args) > 4 && os.Args[1] == "probe" {
		os.Exit(probe(os.Args[2], os.Args[3], os.Args[4]))
	}

	os.Exit(m.Run())
}

func probe(allowedPort, otherPort, file string) int {
	// the test binary is built with cgo for the race detector, so only the
	// calling thread can be confined
	runtime.LockOSThread()

	allowed, _ := strconv.Atoi(allowedPort)

	if err := confine.ApplyToThread([]uint16{uint16(allowed)}); err != nil { //nolint:gosec // a port fits
		fmt.Println("apply:", err)

		return 1
	}

	_, err := os.ReadFile("/etc/hosts")
	report("read /etc/hosts", err)

	_, err = os.ReadFile("/etc/resolv.conf")
	report("read /etc/resolv.conf", err)

	_, err = os.ReadFile(file) //nolint:gosec // the path comes from the test
	report("read own file", err)

	created, err := os.Create(file + ".new") //nolint:gosec // the probe tries what the confinement should stop
	report("create a file", err)
	closeIfOpen(created, err)

	conn, err := net.Dial("tcp", "127.0.0.1:"+allowedPort) //nolint:gosec // the probe tries what the confinement should stop
	report("connect allowed port", err)
	closeIfOpen(conn, err)

	conn, err = net.Dial("tcp", "127.0.0.1:"+otherPort) //nolint:gosec // the probe tries what the confinement should stop
	report("connect other port", err)
	closeIfOpen(conn, err)

	// Landlock lets the kernel pick a port, so the probe names one
	listener, err := net.Listen("tcp", "127.0.0.1:"+otherPort)
	report("listen", err)
	closeIfOpen(listener, err)

	_, err = unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	report("prctl", err)

	nnp, _ := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	fmt.Println("no_new_privs:", nnp)

	// an ordinary process may ask for its personality, the filter says no
	_, _, errno := unix.Syscall(unix.SYS_PERSONALITY, 0xffffffff, 0, 0)
	report("personality", errnoOrNil(errno))

	_, _, errno = unix.Syscall(unix.SYS_BPF, 0, 0, 0)
	report("bpf", errnoOrNil(errno))

	return 0
}

func errnoOrNil(errno unix.Errno) error {
	if errno == 0 {
		return nil
	}

	return errno
}

func closeIfOpen(c interface{ Close() error }, err error) {
	if err == nil {
		_ = c.Close()
	}
}

func report(name string, err error) {
	switch {
	case err == nil:
		fmt.Println(name + ": ok")
	case errors.Is(err, unix.EACCES):
		fmt.Println(name + ": EACCES")
	case errors.Is(err, unix.EPERM):
		fmt.Println(name + ": EPERM")
	default:
		fmt.Println(name+":", err)
	}
}

func needsLandlock(t *testing.T) {
	t.Helper()

	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || abi < 4 {
		t.Skip("no Landlock with network rules")
	}
}

// probeOnce runs the probe one time for all the tests and returns its
// report by name.
var probeOnce = sync.OnceValues(func() (map[string]string, error) {
	allowed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	defer func() { _ = allowed.Close() }()

	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	defer func() { _ = other.Close() }()

	dir, err := os.MkdirTemp("", "aibox-confine-")
	if err != nil {
		return nil, err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	file := filepath.Join(dir, "own")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		return nil, err
	}

	self, err := os.Executable()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(self, "probe", port(allowed), port(other), file) //nolint:gosec // the test runs itself
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, out)
	}

	results := map[string]string{}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		name, result, _ := strings.Cut(scanner.Text(), ": ")
		results[name] = result
	}

	return results, nil
})

func port(listener net.Listener) string {
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

func results(t *testing.T) map[string]string {
	t.Helper()
	needsLandlock(t)

	report, err := probeOnce()
	require.NoError(t, err)

	return report
}

func TestApplyLeavesTheResolverFilesReadable(t *testing.T) {
	// act
	report := results(t)

	// assert
	assert.Equal(t, "ok", report["read /etc/hosts"])
	assert.Equal(t, "ok", report["read /etc/resolv.conf"])
}

func TestApplyTakesTheRestOfTheFileSystemAway(t *testing.T) {
	// act
	report := results(t)

	// assert
	assert.Equal(t, "EACCES", report["read own file"])
	assert.Equal(t, "EACCES", report["create a file"])
}

func TestApplyLeavesOnlyTheAllowedPortsToConnectTo(t *testing.T) {
	// act
	report := results(t)

	// assert
	assert.Equal(t, "ok", report["connect allowed port"])
	assert.Equal(t, "EACCES", report["connect other port"])
	assert.Equal(t, "EACCES", report["listen"])
}

func TestApplyRefusesTheDeniedSyscallsAndLetsOthersThrough(t *testing.T) {
	// act
	report := results(t)

	// assert
	assert.Equal(t, "EPERM", report["personality"])
	assert.Equal(t, "EPERM", report["bpf"])
	assert.Equal(t, "ok", report["prctl"])
}

func TestApplySetsNoNewPrivileges(t *testing.T) {
	// act
	report := results(t)

	// assert
	assert.Equal(t, "1", report["no_new_privs"])
}

func TestApplyRefusesAProgramBuiltWithCgo(t *testing.T) {
	needsLandlock(t)

	// act
	err := confine.Apply(nil)

	// assert
	require.ErrorContains(t, err, "cgo")
}
