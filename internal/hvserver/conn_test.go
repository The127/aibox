package hvserver

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

// calls is what the fake Host Compute Service was asked, in order.
type calls struct {
	mu  sync.Mutex
	got []string
}

func (c *calls) add(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.got = append(c.got, call)
}

func (c *calls) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.got...)
}

// fakeVM is a VM of the fake Host Compute Service.
type fakeVM struct {
	calls        *calls
	startErr     error
	runtimeIDErr error
}

var aRuntimeID = guid.GUID{Data1: 0x1234}

func (v *fakeVM) runtimeID(context.Context) (guid.GUID, error) {
	return aRuntimeID, v.runtimeIDErr
}

func (v *fakeVM) Start(context.Context) error {
	v.calls.add("start")

	return v.startErr
}

func (v *fakeVM) Terminate(ctx context.Context) error {
	if ctx.Err() != nil {
		v.calls.add("terminate with an ended context")

		return ctx.Err()
	}

	v.calls.add("terminate")

	return nil
}

func (v *fakeVM) Close() error {
	v.calls.add("close")

	return nil
}

// fixture is a server with a fake Host Compute Service on a host of 16
// GiB and 8 processors, and the client end of one connection to it.
type fixture struct {
	srv    *server
	calls  *calls
	docs   []hcs.Document
	client net.Conn
	r      *bufio.Reader
	done   chan struct{}
}

func newFixture(t *testing.T, create func(ctx context.Context, f *fixture) (vm, error)) *fixture {
	t.Helper()

	f := &fixture{calls: &calls{}, done: make(chan struct{})}
	f.srv = &server{
		install: hvservice.Install{Kernel: `C:\aibox\vmlinuz`, Rootfs: `C:\aibox\os.vhd`},
		host:    func() (hvservice.Host, error) { return hvservice.Host{MemoryMiB: 16384, CPUs: 8}, nil },
		logf:    t.Logf,
	}
	f.srv.create = func(ctx context.Context, _ string, doc hcs.Document) (vm, error) {
		f.docs = append(f.docs, doc)
		f.calls.add("create")

		if create != nil {
			return create(ctx, f)
		}

		return &fakeVM{calls: f.calls}, nil
	}

	return f
}

// serve serves the connection with ctx until it ends.
func (f *fixture) serve(ctx context.Context, t *testing.T) {
	t.Helper()

	server, client := net.Pipe()
	f.client = client
	f.r = hvservice.NewReader(client)

	go func() {
		defer close(f.done)
		defer func() { _ = server.Close() }()

		f.srv.serve(ctx, server, func() (hvservice.Client, error) {
			return hvservice.Client{User: "S-1-5-21-1-2-3-1001"}, nil
		})
	}()

	t.Cleanup(func() {
		_ = client.Close()
		<-f.done
	})
}

// ask sends the line and returns the reply.
func (f *fixture) ask(t *testing.T, line string) hvservice.Reply {
	t.Helper()

	reply, err := f.tryAsk(line)
	require.NoError(t, err)

	return reply
}

// tryAsk is ask for a goroutine other than the test's.
func (f *fixture) tryAsk(line string) (hvservice.Reply, error) {
	if _, err := f.client.Write([]byte(line + "\n")); err != nil {
		return hvservice.Reply{}, err
	}

	var reply hvservice.Reply
	err := hvservice.Read(f.r, &reply)

	return reply, err
}

// hangUp ends the connection and waits for the server to be done with it.
func (f *fixture) hangUp() {
	_ = f.client.Close()
	<-f.done
}

const createLine = `{"op":"create","create":{"memoryMiB":2048,"cpus":2,"cmdline":"console=ttyS0"}}`

func TestAConnectionMakesAVMStartsItAndEndsIt(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, nil)
	f.serve(t.Context(), t)

	// act
	created := f.ask(t, createLine)
	started := f.ask(t, `{"op":"start"}`)
	f.hangUp()

	// assert
	assert.Empty(t, created.Error)
	assert.Equal(t, aRuntimeID.String(), created.RuntimeID)
	assert.True(t, strings.HasPrefix(created.ConsolePipe, `\\.\pipe\aibox-console-`))
	assert.Equal(t, hvservice.Reply{}, started)
	assert.Equal(t, []string{"create", "start", "terminate", "close"}, f.calls.all())
	assert.Zero(t, f.srv.ledger.used, "the memory of the VM is free again")

	require.Len(t, f.docs, 1)
	assert.Equal(t, "console=ttyS0", f.docs[0].VirtualMachine.Chipset.LinuxKernelDirect.KernelCmdLine)
	assert.Equal(t, created.ConsolePipe, f.docs[0].VirtualMachine.Devices.ComPorts["0"].NamedPipe)
}

func TestAConnectionRefusesARequestOutOfTurn(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"a start before a create": {`{"op":"start"}`},
		"a second create":         {createLine, createLine},
		"a second start":          {createLine, `{"op":"start"}`, `{"op":"start"}`},
	}

	for name, lines := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			f := newFixture(t, nil)
			f.serve(t.Context(), t)

			for _, line := range lines[:len(lines)-1] {
				require.Empty(t, f.ask(t, line).Error)
			}

			// act
			reply := f.ask(t, lines[len(lines)-1])

			// assert
			assert.Contains(t, reply.Error, "out of turn")
		})
	}
}

func TestAConnectionRefusesAVMBeyondTheHost(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, nil)
	f.serve(t.Context(), t)

	// act
	reply := f.ask(t, `{"op":"create","create":{"memoryMiB":2048,"cpus":9,"cmdline":""}}`)

	// assert
	assert.Contains(t, reply.Error, "9 processors")
	assert.Empty(t, f.calls.all(), "no VM was made")
}

func TestAConnectionAnswersALineThatIsNoMessageAndGoesOn(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, nil)
	f.serve(t.Context(), t)

	// act
	bad := f.ask(t, `{"op":`)
	created := f.ask(t, createLine)

	// assert
	assert.Contains(t, bad.Error, "not a message of the protocol")
	assert.Empty(t, created.Error)
}

func TestAConnectionEndsAfterAMessageTooLong(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, nil)
	f.serve(t.Context(), t)

	// a MiB is far more than any message, which takes some KiB
	go func() {
		_, _ = f.client.Write([]byte(`{"op":"` + strings.Repeat("x", 1<<20) + "\"}\n"))
	}()

	// act
	var reply hvservice.Reply
	require.NoError(t, hvservice.Read(f.r, &reply))

	// assert
	assert.Contains(t, reply.Error, "too long")
	<-f.done
}

func TestAVMThatFailsToBeMadeTakesNoMemory(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, func(context.Context, *fixture) (vm, error) {
		return nil, &hcs.Error{Op: "HcsCreateComputeSystem", HRESULT: 0x80070005}
	})
	f.serve(t.Context(), t)

	// act
	reply := f.ask(t, createLine)

	// assert
	assert.Contains(t, reply.Error, "HRESULT 0x80070005")
	assert.Zero(t, f.srv.ledger.used)
}

func TestAVMEndsThoughTheServicesContextHas(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, nil)
	ctx, stop := context.WithCancel(t.Context())
	f.serve(ctx, t)
	require.Empty(t, f.ask(t, createLine).Error)

	// act: the service stops, and closes the connection to stop the read
	stop()
	f.hangUp()

	// assert
	assert.Equal(t, []string{"create", "terminate", "close"}, f.calls.all(), "the VM ended though the service's context had")
}

func TestStoppingTheServiceStopsAVMBeingMade(t *testing.T) {
	t.Parallel()

	// arrange
	making := make(chan struct{})
	f := newFixture(t, func(ctx context.Context, _ *fixture) (vm, error) {
		close(making)
		<-ctx.Done()

		return nil, errors.New("the call was cancelled")
	})
	ctx, stop := context.WithCancel(t.Context())
	f.serve(ctx, t)

	type answer struct {
		reply hvservice.Reply
		err   error
	}

	replied := make(chan answer, 1)

	go func() {
		reply, err := f.tryAsk(createLine)
		replied <- answer{reply, err}
	}()

	<-making

	// act
	stop()

	// assert
	a := <-replied
	require.NoError(t, a.err)
	assert.NotEmpty(t, a.reply.Error)
	assert.Zero(t, f.srv.ledger.used)
}

func TestAVMThatFailsHalfwayEndsAndLetsTheClientMakeAnother(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		vm    fakeVM
		lines []string
		want  []string
	}{
		"its runtime id unread": {
			vm:    fakeVM{runtimeIDErr: errors.New("no properties")},
			lines: []string{createLine},
			want:  []string{"create", "terminate", "close"},
		},
		"not started": {
			vm:    fakeVM{startErr: &hcs.Error{Op: "HcsStartComputeSystem", HRESULT: 0x80070005}},
			lines: []string{createLine, `{"op":"start"}`},
			want:  []string{"create", "start", "terminate", "close"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			first := true
			f := newFixture(t, func(_ context.Context, f *fixture) (vm, error) {
				if !first {
					return &fakeVM{calls: f.calls}, nil
				}

				first = false
				v := tt.vm
				v.calls = f.calls

				return &v, nil
			})
			f.serve(t.Context(), t)

			for _, line := range tt.lines[:len(tt.lines)-1] {
				require.Empty(t, f.ask(t, line).Error)
			}

			// act
			failed := f.ask(t, tt.lines[len(tt.lines)-1])

			// assert
			assert.NotEmpty(t, failed.Error)
			assert.Equal(t, tt.want, f.calls.all(), "the VM ended")
			assert.Zero(t, f.srv.ledger.used, "and gave its memory back")
			assert.Empty(t, f.ask(t, createLine).Error, "the client may make another")
		})
	}
}
