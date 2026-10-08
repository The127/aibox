//go:build linux

package launch_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/launch"
)

// The test runs aibox as a process of its own, since killing it is the
// point, and checks that the kernel ends the programs it started.
func TestKilledAiboxTakesVirtiofsdAndQEMUWithoutTheSandboxAlong(t *testing.T) {
	// arrange
	f := fakes(t)
	t.Setenv("AIBOX_FAKE_QEMU", "wait")

	stderr := &syncBuffer{}
	aibox := exec.Command(f.aibox) //nolint:gosec // the link to the test binary
	aibox.Stderr = stderr
	// fakes that outlive aibox hold the pipe of its standard error
	aibox.WaitDelay = time.Second
	require.NoError(t, aibox.Start())

	var pids []int

	require.Eventually(t, func() bool {
		pids = nil

		for _, name := range []string{"qemu-pid", "virtiofsd-project.sock-pid", "virtiofsd-home.sock-pid"} {
			content, err := os.ReadFile(filepath.Join(f.records, name)) //nolint:gosec // records is a temp folder of the test
			if err != nil {
				return false
			}

			pid, err := strconv.Atoi(string(content))
			if err != nil {
				return false
			}

			pids = append(pids, pid)
		}

		return true
	}, 10*time.Second, 10*time.Millisecond, "the fakes did not start: %s", stderr)

	// a failed test leaves no fakes behind
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	// act
	require.NoError(t, aibox.Process.Kill())
	_ = aibox.Wait()

	// assert
	for _, pid := range pids {
		require.Eventually(t, func() bool { return !alive(pid) }, 5*time.Second, 10*time.Millisecond,
			"process %d still runs after aibox was killed", pid)
	}
}

// A child must not die when the thread of the goroutine that started it
// ends, which Go does when a goroutine locked to its thread returns.
func TestStartedChildOutlivesTheThreadThatAskedForIt(t *testing.T) {
	// arrange
	f := fakes(t)
	t.Setenv("AIBOX_FAKE_QEMU", "wait")

	child := exec.Command(f.qemu) //nolint:gosec // the link to the test binary
	child.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	started := make(chan error, 1)

	// act
	go func() {
		// returning without unlocking ends the thread
		runtime.LockOSThread()

		started <- launch.Start(child)
	}()

	require.NoError(t, <-started)

	exited := make(chan error, 1)

	go func() { exited <- child.Wait() }()

	// assert
	select {
	case err := <-exited:
		t.Fatalf("the child ended with the thread: %v", err)
	case <-time.After(500 * time.Millisecond):
	}

	require.NoError(t, child.Process.Signal(syscall.SIGTERM))
	<-exited
}

// alive is a process that exists and is not a zombie.
func alive(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}

	// the state follows the name, which is in parentheses and may hold
	// spaces of its own
	_, rest, ok := strings.Cut(string(stat), ") ")

	return ok && !strings.HasPrefix(rest, "Z") && !strings.HasPrefix(rest, "X")
}
