package vsockns_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/vsockns"
)

// The test binary is also the helper that Open starts, picked by its first
// argument, as aibox itself is.
func TestMain(m *testing.M) {
	switch {
	case len(os.Args) > 1 && os.Args[1] == "helper":
		if err := vsockns.Serve(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		os.Exit(0)
	case len(os.Args) > 1 && os.Args[1] == "failing-helper":
		fmt.Fprintln(os.Stderr, "the helper broke")
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// needsVsockNamespaces skips the test on a machine without vsock, vsock
// namespaces or unprivileged user namespaces.
func needsVsockNamespaces(t *testing.T) {
	t.Helper()

	for _, path := range []string{"/dev/vhost-vsock", "/proc/sys/net/vsock/child_ns_mode"} {
		if _, err := os.Stat(path); err != nil {
			t.Skipf("no %s", path)
		}
	}

	if limit, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err != nil || strings.TrimSpace(string(limit)) == "0" {
		t.Skip("no unprivileged user namespaces")
	}
}

func closeVsock(v *vsockns.Vsock) {
	_ = v.Proxy.Close()
	_ = v.Terminal.Close()
	_ = v.Vhost.Close()
}

func TestOpenGivesTheVMAVsockOfItsOwn(t *testing.T) {
	needsVsockNamespaces(t)

	// act
	vsock, err := vsockns.Open("helper")

	// assert
	require.NoError(t, err)
	t.Cleanup(func() { closeVsock(vsock) })
	assert.NotZero(t, vsock.ProxyPort)
	assert.NotZero(t, vsock.TerminalPort)
	assert.NotEqual(t, vsock.ProxyPort, vsock.TerminalPort)
	assert.Equal(t, "vsock", vsock.Proxy.Addr().Network())
	assert.Equal(t, "vsock", vsock.Terminal.Addr().Network())

	info, err := vsock.Vhost.Stat()
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeCharDevice)
}

func TestOpenWorksForASecondVMAtTheSameTime(t *testing.T) {
	needsVsockNamespaces(t)

	// arrange
	first, err := vsockns.Open("helper")
	require.NoError(t, err)
	t.Cleanup(func() { closeVsock(first) })

	// act
	second, err := vsockns.Open("helper")

	// assert
	require.NoError(t, err)
	t.Cleanup(func() { closeVsock(second) })
	assert.NotNil(t, second.Proxy)
}

func TestOpenReportsAHelperThatFails(t *testing.T) {
	needsVsockNamespaces(t)

	// act
	_, err := vsockns.Open("failing-helper")

	// assert
	require.Error(t, err)
	assert.ErrorContains(t, err, "the helper broke")
}
