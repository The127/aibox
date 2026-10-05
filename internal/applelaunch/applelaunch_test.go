package applelaunch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/applelaunch"
	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/link"
	"github.com/the127/aibox/internal/proxy"
)

// fake is the fake container tool of a test and the folders of its run.
type fake struct {
	dir     string
	backend applelaunch.Backend
	spec    backend.Spec
	screen  *syncBuffer
}

func newFake(t *testing.T) *fake {
	t.Helper()

	f := &fake{dir: t.TempDir(), screen: &syncBuffer{}}
	t.Setenv(fakeEnv, f.dir)

	image := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(image, "container-image"), []byte("aibox:test\n"), 0o600))

	f.backend = applelaunch.Backend{Program: os.Args[0], BootTimeout: 10 * time.Second, SessionEndDelay: 2 * time.Second}
	f.spec = backend.Spec{
		Image:      image,
		State:      filepath.Join(t.TempDir(), "state.ext4"),
		StateBytes: 1 << 30,
		MemoryMiB:  1024,
		CPUs:       3,
		Project:    t.TempDir(),
		Home:       t.TempDir(),
		ConsoleLog: filepath.Join(t.TempDir(), "console.log"),
		Stdout:     f.screen,
		Stderr:     f.screen,
	}

	return f
}

func (f *fake) calls(t *testing.T) []string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(f.dir, "calls"))
	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(content)), "\n")
}

// ranTheVM says whether the tool was asked to run the VM yet.
func (f *fake) ranTheVM() bool {
	content, _ := os.ReadFile(filepath.Join(f.dir, "calls"))

	return strings.Contains(string(content), "\nrun ") || strings.HasPrefix(string(content), "run ")
}

func (f *fake) runCall(t *testing.T) string {
	t.Helper()

	for _, call := range f.calls(t) {
		if strings.HasPrefix(call, "run ") {
			return call
		}
	}

	t.Fatal("the tool was never asked to run the VM")

	return ""
}

func (f *fake) console(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(f.spec.ConsoleLog)
	require.NoError(t, err)

	return string(content)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestCheckImageFindsTheImageTheFolderNames(t *testing.T) {
	// arrange
	f := newFake(t)

	// act
	err := f.backend.CheckImage(f.spec.Image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"image inspect aibox:test"}, f.calls(t))
}

func TestCheckImageWithoutTheFileSaysHowToBuildTheImage(t *testing.T) {
	// arrange
	f := newFake(t)

	// act
	err := f.backend.CheckImage(t.TempDir())

	// assert
	require.ErrorIs(t, err, applelaunch.ErrNoImage)
	assert.ErrorContains(t, err, "just install-container-image")
}

func TestCheckImageOfAnImageTheToolDoesNotHave(t *testing.T) {
	// arrange
	f := newFake(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.spec.Image, "container-image"), []byte("aibox:gone\n"), 0o600))

	// act
	err := f.backend.CheckImage(f.spec.Image)

	// assert
	require.ErrorIs(t, err, applelaunch.ErrNoImage)
	assert.ErrorContains(t, err, "aibox:gone")
}

func TestRunShowsTheSessionAndEndsWithItsExitCode(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeOutput, "hello from the fake VM")
	t.Setenv(fakeCode, "3")

	// act
	err := f.backend.Run(context.Background(), f.spec)

	// assert
	assert.Equal(t, &backend.ExitError{Code: 3}, err)
	assert.Contains(t, f.screen.String(), "hello from the fake VM")
	assert.Contains(t, f.console(t), "booting the fake VM")
}

func TestRunEndsWithoutAnErrorWhenTheCommandDoes(t *testing.T) {
	// arrange
	f := newFake(t)

	// act
	err := f.backend.Run(context.Background(), f.spec)

	// assert
	assert.NoError(t, err)
}

func TestRunHandsTheSpecToTheTool(t *testing.T) {
	// arrange
	f := newFake(t)
	f.spec.Shell = true
	f.spec.Mounts = []backend.Mount{{Host: f.spec.Home, Guest: "/opt/go"}}

	// act
	require.NoError(t, f.backend.Run(context.Background(), f.spec))

	// assert
	run := f.runCall(t)
	assert.Contains(t, run, "--cpus 3 --memory 1024M")
	assert.Contains(t, run, "--mount type=bind,source="+f.spec.Project+",target=/project ")
	assert.Contains(t, run, "--mount type=bind,source="+f.spec.Home+",target=/opt/go,readonly")
	assert.Contains(t, run, "--kernel-arg aibox.shell")
	assert.True(t, strings.HasSuffix(run, " -- aibox:test"), run)
}

func TestRunMakesTheStateVolumeOnceWithTheSize(t *testing.T) {
	// arrange
	f := newFake(t)

	// act
	require.NoError(t, f.backend.Run(context.Background(), f.spec))
	require.NoError(t, f.backend.Run(context.Background(), f.spec))

	// assert
	var creates []string

	for _, call := range f.calls(t) {
		if strings.HasPrefix(call, "volume create") {
			creates = append(creates, call)
		}
	}

	require.Len(t, creates, 1)
	assert.True(t, strings.HasPrefix(creates[0], "volume create -s 1073741824 aibox-state-"), creates[0])
	assert.Contains(t, f.runCall(t), "--volume "+strings.Fields(creates[0])[4]+":/var/lib/aibox/state")
}

func TestRunConnectsAgainWhileTheGuestDoesNotListenYet(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeDrops, "3")
	t.Setenv(fakeCode, "4")

	// act
	err := f.backend.Run(context.Background(), f.spec)

	// assert
	assert.Equal(t, &backend.ExitError{Code: 4}, err)
}

func TestRunServesTheProxyOverTheLink(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeProxy, "1")

	var refused []string

	var mu sync.Mutex

	f.spec.Proxy = proxy.Options{
		Allow:     func(string, string) bool { return false },
		OnRefused: func(target string) { mu.Lock(); refused = append(refused, target); mu.Unlock() },
	}

	// act
	require.NoError(t, f.backend.Run(context.Background(), f.spec))

	// assert
	answer, err := os.ReadFile(filepath.Join(f.dir, "proxy"))
	require.NoError(t, err)
	assert.Equal(t, "403 Forbidden", string(answer))
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"evil.example:443"}, refused)
	assert.NotContains(t, f.screen.String(), "proxy stopped")
}

func TestRunSaysWhereToLookWhenTheVMEndsBeforeItsTerminal(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeRun, "fail")

	// act
	err := f.backend.Run(context.Background(), f.spec)

	// assert
	require.ErrorIs(t, err, host.ErrNoTerminal)
	assert.ErrorContains(t, err, f.spec.ConsoleLog)
	assert.Contains(t, f.console(t), "operation not permitted")
}

func TestRunGivesUpOnAVMThatDoesNotComeUpAndStopsIt(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeRun, "hang")
	f.backend.BootTimeout = 300 * time.Millisecond

	// act
	err := f.backend.Run(context.Background(), f.spec)

	// assert
	require.ErrorIs(t, err, applelaunch.ErrNoBoot)
	assert.ErrorContains(t, err, "see "+f.spec.ConsoleLog)
	assert.Contains(t, f.calls(t), "kill "+nameOf(f.runCall(t)))
}

func TestRunStopsTheVMWhenTheContextEnds(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeRun, "hang")

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		for !f.ranTheVM() {
			time.Sleep(10 * time.Millisecond)
		}

		cancel()
	}()

	// act
	err := f.backend.Run(ctx, f.spec)

	// assert
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, f.calls(t), "kill "+nameOf(f.runCall(t)))
}

func TestRunLeavesNoSocketFolderBehind(t *testing.T) {
	// arrange
	f := newFake(t)

	// act
	require.NoError(t, f.backend.Run(context.Background(), f.spec))

	// assert
	socket := ""
	fields := strings.Fields(f.runCall(t))

	for i, field := range fields {
		if field == "--publish-socket" {
			socket, _, _ = strings.Cut(fields[i+1], ":")
		}
	}

	require.NotEmpty(t, socket)
	assert.LessOrEqual(t, len(socket), 100, "macOS takes socket paths up to 104 bytes")
	assert.NoDirExists(t, filepath.Dir(socket))
}

// nameOf is the name the run gave the container.
func nameOf(run string) string {
	fields := strings.Fields(run)
	for i, field := range fields {
		if field == "--name" {
			return fields[i+1]
		}
	}

	return ""
}

func TestRunEndsWithTheContextWhileItMakesTheVolume(t *testing.T) {
	// arrange
	f := newFake(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// act
	err := f.backend.Run(ctx, f.spec)

	// assert
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRunRefusesASecondRunOfTheProject(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeRun, "hang")
	f.backend.BootTimeout = time.Minute

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)

	go func() { first <- f.backend.Run(ctx, f.spec) }()

	for !f.ranTheVM() {
		time.Sleep(10 * time.Millisecond)
	}

	// act
	err := f.backend.Run(context.Background(), f.spec)

	// assert
	require.ErrorIs(t, err, applelaunch.ErrBusy)
	assert.ErrorContains(t, err, "another aibox runs this project")

	runs := 0

	for _, call := range f.calls(t) {
		if strings.HasPrefix(call, "run ") {
			runs++
		}
	}

	assert.Equal(t, 1, runs)

	cancel()
	assert.ErrorIs(t, <-first, context.Canceled)
}

func TestWithoutTheToolItSaysToInstallItNotToBuildAnImage(t *testing.T) {
	// arrange
	f := newFake(t)
	f.backend.Program = filepath.Join(t.TempDir(), "container")

	// act
	checked := f.backend.CheckImage(f.spec.Image)
	ran := f.backend.Run(context.Background(), f.spec)

	// assert
	for _, err := range []error{checked, ran} {
		require.ErrorIs(t, err, applelaunch.ErrNoTool)
		assert.NotErrorIs(t, err, applelaunch.ErrNoImage)
	}
}

func TestRunGivesUpInTimeOnAToolThatHoldsTheConnectionSilently(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeRun, "hold")
	f.backend.BootTimeout = 300 * time.Millisecond
	started := time.Now()

	// act
	err := f.backend.Run(context.Background(), f.spec)

	// assert
	require.ErrorIs(t, err, applelaunch.ErrNoBoot)
	assert.Less(t, time.Since(started), 5*time.Second, "the run waited out the greeting")
	assert.Contains(t, f.calls(t), "kill "+nameOf(f.runCall(t)))
}

func TestRunStopsAtOnceWhenCancelledWhileItWaitsForTheGreeting(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeRun, "hold")

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		for !f.ranTheVM() {
			time.Sleep(10 * time.Millisecond)
		}

		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	started := time.Now()

	// act
	err := f.backend.Run(ctx, f.spec)

	// assert
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(started), 5*time.Second, "the run waited out the greeting")
}

func TestRunKillsTheVMWhenSomethingElseThanTheGuestAnswers(t *testing.T) {
	// arrange
	f := newFake(t)
	t.Setenv(fakeRun, "garbage")

	// act
	err := f.backend.Run(context.Background(), f.spec)

	// assert
	require.ErrorIs(t, err, link.ErrNotAGuest)
	assert.Contains(t, f.calls(t), "kill "+nameOf(f.runCall(t)))
}
