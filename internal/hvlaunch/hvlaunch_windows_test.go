package hvlaunch

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/hvservice"
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
	// stateGoneAtStop tells whether the state disk's name was gone when
	// the VM was stopped, as a task's should be
	stateGoneAtStop bool
	state           string
	onStop          func()
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
		service: &fakeService{state: filepath.Join(dir, "state.ext4")},
		console: newFed(),
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

			return &closing{Listener: l, closed: make(chan struct{})}, nil
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
