package hvlaunch

import (
	"bytes"
	"context"
	"errors"
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

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/appcontainer"
	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/hvservice"
	"github.com/the127/aibox/internal/hvshares"
	"github.com/the127/aibox/internal/hvsock"
	"github.com/the127/aibox/internal/hvvm"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/vhd"
)

// aConsoleHandle is the number the fake service says the console pipe
// is under in this process.
const aConsoleHandle = 7

// fakeService answers as the service does, over a pipe in memory, and
// records what it was asked.
type fakeService struct {
	mu   sync.Mutex
	ops  []hvservice.Op
	made *hvservice.Create
	// refuse is what the service refuses a create with, refuseStart what
	// it refuses a start with, if anything
	refuse      string
	refuseStart string
	// old is a service older than aibox, which makes no container
	old bool
	// stateGoneAtStop tells whether the state disk's name was gone when
	// the VM was stopped, as a task's should be
	stateGoneAtStop bool
	state           string
	onStop          func()
	// onStart is called as the VM starts, once the helpers serve it
	onStart func()
}

func (s *fakeService) dial(context.Context) (net.Conn, error) {
	server, client := net.Pipe()

	go s.serve(server)

	return client, nil
}

func (s *fakeService) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	r := hvservice.NewReader(conn)

	for {
		var request hvservice.Request
		if err := hvservice.Read(r, &request); err != nil {
			return
		}

		s.mu.Lock()
		s.ops = append(s.ops, request.Op)

		reply := hvservice.Reply{}

		switch request.Op {
		case hvservice.OpCreate:
			s.made = request.Create

			if s.refuse != "" {
				reply.Error = s.refuse
			} else {
				id, _ := guid.NewV4()
				reply = hvservice.Reply{RuntimeID: id.String(), Console: aConsoleHandle}

				if request.Create.Network != nil && !s.old {
					reply.Container, reply.ContainerSID = "aibox.the-container", "S-1-15-2-1"
				}
			}
		case hvservice.OpStop:
			_, err := os.Stat(s.state)
			s.stateGoneAtStop = errors.Is(err, os.ErrNotExist)

			// as Hyper-V lets go of the console of a VM that is gone
			if s.onStop != nil {
				s.onStop()
			}

		case hvservice.OpStart:
			reply.Error = s.refuseStart

			if s.onStart != nil {
				s.onStart()
			}
		}

		s.mu.Unlock()

		if err := hvservice.Write(conn, reply); err != nil {
			return
		}
	}
}

func (s *fakeService) asked() []hvservice.Op {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]hvservice.Op(nil), s.ops...)
}

// fixture is a backend on a fake service, with the ports of the VM on
// loopback and the console on a listener the test feeds.
type fixture struct {
	backend Backend
	service *fakeService
	console *fed
	spec    backend.Spec
	// shared is what the helper of the shares was given
	shared       *hvshares.Config
	sharesHelper *fakeHelper
	// sandbox is the container the helper of the proxy was started in, if
	// any, and proxied what it was given
	sandbox     *container
	proxied     *hvvm.Config
	proxyHelper *fakeHelper
	// swept is how often the containers of runs that ended were swept,
	// sweptBefore how often before the helper of the proxy started, and
	// sweepFails what a sweep fails with, if anything
	swept, sweptBefore int
	sweepFails         error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	dir := t.TempDir()
	image := filepath.Join(dir, "image")
	require.NoError(t, os.Mkdir(image, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(image, "vmlinuz"), []byte("kernel"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(image, "os.ext4"), make([]byte, 4096), 0o600))

	for _, folder := range []string{"project", "home"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, folder), 0o750))
	}

	f := &fixture{
		sharesHelper: &fakeHelper{done: make(chan struct{})},
		proxyHelper:  &fakeHelper{done: make(chan struct{})},
		service:      &fakeService{state: filepath.Join(dir, "state.ext4")},
		console:      newFed(),
		spec: backend.Spec{
			Image:      image,
			State:      filepath.Join(dir, "state.ext4"),
			StateBytes: 1 << 20,
			MemoryMiB:  2048,
			CPUs:       2,
			Project:    filepath.Join(dir, "project"),
			Home:       filepath.Join(dir, "home"),
			ConsoleLog: filepath.Join(dir, "console.log"),
			Stdout:     &bytes.Buffer{},
		},
	}

	t.Cleanup(func() { _ = f.console.Close() })

	f.backend = Backend{
		BootTimeout:     200 * time.Millisecond,
		SessionEndDelay: time.Second,
		PowerOffWait:    200 * time.Millisecond,
		dial:            f.service.dial,
		listen: func(guid.GUID, uint32) (net.Listener, error) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, err
			}

			return hvsock.Closing(l), nil
		},
		shares: func(_ context.Context, _ string, _ *appcontainer.Job, _ io.Writer, c hvshares.Config) (helper, error) {
			f.shared = &c

			return f.sharesHelper, nil
		},
		proxy: func(_ context.Context, c *container, _ hvvm.Spec, config hvvm.Config) (helper, error) {
			f.sandbox, f.proxied, f.sweptBefore = c, &config, f.swept

			return f.proxyHelper, nil
		},
		sweep: func() error {
			f.swept++

			return f.sweepFails
		},
		console: func(handle uint64) consoleSource {
			if handle != aConsoleHandle {
				t.Errorf("the console was taken under handle %d, not the service's %d", handle, aConsoleHandle)
			}

			return f.console
		},
	}

	return f
}

func TestRunStopsAVMWhoseGuestNeverConnects(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	require.ErrorIs(t, err, ErrNoBoot)
	assert.ErrorContains(t, err, "console.log")
	assert.Equal(t, []hvservice.Op{hvservice.OpCreate, hvservice.OpStart, hvservice.OpStop}, f.service.asked())

	made := f.service.made
	require.NotNil(t, made)
	assert.Equal(t, 2048, made.MemoryMiB)
	assert.Equal(t, 2, made.CPUs)
	assert.NotZero(t, made.State, "the handle of the state disk")
	assert.True(t, strings.HasPrefix(made.Cmdline, baseCmdline+" "), made.Cmdline)
	assert.Contains(t, made.Cmdline, " aibox.hyperv aibox.proxy=1025 aibox.terminal=1024")

	is, err := vhd.IsDynamic(f.spec.State)
	require.NoError(t, err)
	assert.True(t, is, "the state disk is a dynamic VHD")

	require.NotNil(t, f.shared, "a helper serves the shares")
	assert.Equal(t, uint32(sharesPort), f.shared.Port)
	assert.NotEmpty(t, f.shared.Shares)
}

func TestRunEndsWhenTheVMIsGone(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.backend.BootTimeout = time.Minute

	ran := make(chan error, 1)

	go func() { ran <- f.backend.Run(t.Context(), f.spec) }()

	hyperV, guest := net.Pipe()
	f.console.conns <- hyperV

	_, err := guest.Write([]byte("[    0.000000] Linux\n"))
	require.NoError(t, err)

	// act: the VM ends, and Hyper-V lets go of its console
	require.NoError(t, guest.Close())

	// assert
	select {
	case err := <-ran:
		assert.ErrorIs(t, err, host.ErrNoTerminal)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not end with the VM")
	}

	log, err := os.ReadFile(f.spec.ConsoleLog)
	require.NoError(t, err)
	assert.Equal(t, "[    0.000000] Linux\n", string(log))
	assert.Equal(t, []hvservice.Op{hvservice.OpCreate, hvservice.OpStart, hvservice.OpStop}, f.service.asked(), "the service lets go of the VM that ended")
}

func TestRunStopsTheVMWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.backend.BootTimeout = time.Minute

	ctx, cancel := context.WithCancel(t.Context())
	ran := make(chan error, 1)

	go func() { ran <- f.backend.Run(ctx, f.spec) }()

	// a console that runs, so that the VM is not taken for gone, until the
	// service has ended the VM
	hyperV, guest := net.Pipe()
	f.service.onStop = func() { _ = guest.Close() }
	f.console.conns <- hyperV

	// act
	cancel()

	// assert
	select {
	case err := <-ran:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not end with the context")
	}

	assert.Equal(t, []hvservice.Op{hvservice.OpCreate, hvservice.OpStart, hvservice.OpStop}, f.service.asked())
}

func TestRunTellsWhatTheServiceRefused(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.service.refuse = "bad request: 9 processors, not between 1 and the 8 the host has"

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	assert.ErrorContains(t, err, "9 processors, not between 1 and the 8 the host has")
	assert.Equal(t, []hvservice.Op{hvservice.OpCreate}, f.service.asked(), "nothing was started or stopped")
}

func TestRunDoesNotStopAVMThatDidNotStart(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.service.refuseStart = "the service could not start the VM: HRESULT 0xC03A001A"

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	assert.ErrorContains(t, err, "HRESULT 0xC03A001A")
	assert.Equal(t, []hvservice.Op{hvservice.OpCreate, hvservice.OpStart}, f.service.asked(), "the service ended the VM that did not start")
}

func TestRunRefusesAStateDiskAnotherVMHasOpen(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	require.NoError(t, os.WriteFile(f.spec.State, make([]byte, 1<<20), 0o600))
	hyperVOpen(t, f.spec.State)

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	assert.ErrorIs(t, err, machine.ErrStateBusy)
	assert.Empty(t, f.service.asked())
}

func TestRunRemovesTheStateDiskOfATaskWhileTheVMHasIt(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	task := filepath.Join(t.TempDir(), "task")
	require.NoError(t, os.Mkdir(task, 0o750))

	f.spec.Task = task
	f.spec.Progress = &bytes.Buffer{}
	f.spec.RemoveState = true

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	require.ErrorIs(t, err, ErrNoBoot)
	assert.True(t, f.service.stateGoneAtStop, "the state disk's name was gone while the VM ran")
	assert.NoFileExists(t, f.spec.State)
}

func TestRunRefusesACommandLineTheKernelWouldCut(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.spec.Mounts = []backend.Mount{{Host: f.spec.Project, Guest: "/opt/" + strings.Repeat("x", hvservice.MaxCmdline)}}

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	assert.ErrorContains(t, err, "fewer mounts")
	assert.Empty(t, f.service.asked())
}

// fakeHelper is a helper of the run, which ends when done is closed.
type fakeHelper struct {
	done chan struct{}
}

func (h *fakeHelper) Close() error          { return nil }
func (h *fakeHelper) Done() <-chan struct{} { return h.done }

func TestRunStopsTheVMWhenTheHelperOfTheSharesEnds(t *testing.T) {
	t.Parallel()

	// arrange: a VM that runs, with a console that runs
	f := newFixture(t)
	f.backend.BootTimeout = time.Minute

	ran := make(chan error, 1)

	go func() { ran <- f.backend.Run(t.Context(), f.spec) }()

	hyperV, guest := net.Pipe()
	f.service.onStop = func() { _ = guest.Close() }
	f.console.conns <- hyperV

	// act
	close(f.sharesHelper.done)

	// assert
	select {
	case err := <-ran:
		assert.ErrorIs(t, err, ErrSharesGone)
	case <-time.After(10 * time.Second):
		t.Fatal("Run outlived the helper of the shares")
	}

	assert.Contains(t, f.service.asked(), hvservice.OpStop)
}

func TestRunRefusesAShareThatIsNoFolderBeforeItMakesAnything(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.spec.Mounts = []backend.Mount{{Host: filepath.Join(t.TempDir(), "none"), Guest: "/opt/none"}}

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, f.service.asked())
}

func TestRunServesTheProxyFromAHelperInTheContainerTheServiceLetReachTheAllowList(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.spec.AllowList = []string{"example.com:443", "git.example.com:22", "127.0.0.1:8080"}
	f.spec.AllowFile = `C:\Users\me\.aibox\projects\repo\config.yaml`

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	require.ErrorIs(t, err, ErrNoBoot)

	require.NotNil(t, f.service.made.Network, "the service is asked for the network of the container")
	assert.Equal(t, []uint16{22, 443}, f.service.made.Network.Ports)
	assert.Equal(t, []uint16{8080}, f.service.made.Network.Loopback)

	assert.Equal(t, &container{name: "aibox.the-container", sid: "S-1-15-2-1"}, f.sandbox, "the container the service named")
	require.NotNil(t, f.proxied)
	assert.Equal(t, uint32(proxyPort), f.proxied.ProxyPort)
	assert.Equal(t, f.spec.AllowList, f.proxied.AllowList)
	assert.Equal(t, f.spec.AllowFile, f.proxied.AllowFile)
	assert.Empty(t, f.proxied.Local, "no services")
}

func TestRunHandsTheServicesToTheHelperOnALoopbackPortOfItsOwn(t *testing.T) {
	t.Parallel()

	// arrange: a service on port 3129 of the loopback of the VM
	f := newFixture(t)
	f.spec.Loopback = []uint16{8080, 3129}
	f.spec.AllowList = []string{"127.0.0.1:8080"}
	f.spec.Proxy.Local = func(host, port string) http.Handler {
		if host != "localhost" || port != "3129" {
			return nil
		}

		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "answered") })
	}

	answered := make(chan string, 1)
	f.service.onStart = func() {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+f.proxied.Services+"/", nil)
		req.Header.Set("Aibox-Token", f.proxied.Token)
		req.Header.Set("Aibox-Port", "3129")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			answered <- err.Error()

			return
		}

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		answered <- string(body)
	}

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	require.ErrorIs(t, err, ErrNoBoot)
	assert.Equal(t, "answered", <-answered, "aibox answers what the helper hands it")
	assert.Equal(t, []uint16{3129}, f.proxied.Local)
	assert.NotEmpty(t, f.proxied.Token)

	_, port, err := net.SplitHostPort(f.proxied.Services)
	require.NoError(t, err)
	assert.Contains(t, f.service.made.Network.Loopback, mustPort(t, port), "which the container may reach")
	assert.Contains(t, f.service.made.Network.Loopback, uint16(8080))
}

func mustPort(t *testing.T, port string) uint16 {
	t.Helper()

	n, err := strconv.ParseUint(port, 10, 16)
	require.NoError(t, err)

	return uint16(n)
}

func TestRunWithoutTheSandboxServesTheProxyFromAHelperInNoContainer(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.spec.Unsandboxed = true

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	require.ErrorIs(t, err, ErrNoBoot)
	assert.Nil(t, f.service.made.Network)
	require.NotNil(t, f.proxied, "a helper serves the proxy")
	assert.Nil(t, f.sandbox, "in no container")
	assert.Zero(t, f.swept, "no container to sweep for")
}

func TestRunRefusesAServiceThatMakesNoContainer(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.service.old = true

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	require.ErrorIs(t, err, ErrOldService)
	assert.Nil(t, f.proxied, "no helper runs outside the container")
	assert.Equal(t, []hvservice.Op{hvservice.OpCreate}, f.service.asked())
}

func TestRunStopsTheVMWhenTheHelperOfTheProxyEnds(t *testing.T) {
	t.Parallel()

	// arrange: a VM that runs, with a console that runs
	f := newFixture(t)
	f.backend.BootTimeout = time.Minute

	ran := make(chan error, 1)

	go func() { ran <- f.backend.Run(t.Context(), f.spec) }()

	hyperV, guest := net.Pipe()
	f.service.onStop = func() { _ = guest.Close() }
	f.console.conns <- hyperV

	// act
	close(f.proxyHelper.done)

	// assert
	select {
	case err := <-ran:
		assert.ErrorIs(t, err, ErrProxyGone)
	case <-time.After(10 * time.Second):
		t.Fatal("Run outlived the helper of the proxy")
	}

	assert.Contains(t, f.service.asked(), hvservice.OpStop)
}

func TestRunSweepsTheContainersOfRunsThatEndedBeforeItStartsTheHelper(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	require.ErrorIs(t, err, ErrNoBoot)
	assert.Equal(t, 1, f.swept)
	assert.Equal(t, 1, f.sweptBefore, "before the helper of the proxy started")
}

func TestRunStartsWhenTheSweepFails(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t)
	f.sweepFails = errors.New("access denied")

	var stderr bytes.Buffer

	f.spec.Stderr = &stderr

	// act
	err := f.backend.Run(t.Context(), f.spec)

	// assert
	require.ErrorIs(t, err, ErrNoBoot, "the run went on")
	require.NotNil(t, f.proxied, "a helper serves the proxy")
	assert.Contains(t, stderr.String(), "remove the containers of runs that ended: access denied")
}
