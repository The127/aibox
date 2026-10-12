package hvserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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
	srv     *server
	calls   *calls
	pipes   *calls
	docs    []hcs.Document
	diskErr error
	granted []string
	// contained makes the client one in an AppContainer, and networkErr
	// fails the filters of the network
	contained  bool
	networkErr error
	exemptErr  error
	client     net.Conn
	r          *bufio.Reader
	done       chan struct{}
}

func newFixture(t *testing.T, create func(ctx context.Context, f *fixture) (vm, error)) *fixture {
	t.Helper()

	f := &fixture{calls: &calls{}, pipes: &calls{}, done: make(chan struct{})}
	f.srv = &server{
		install: hvservice.Install{Kernel: `C:\aibox\vmlinuz`, Rootfs: `C:\aibox\os.vhd`},
		host:    func() (hvservice.Host, error) { return hvservice.Host{MemoryMiB: 16384, CPUs: 8}, nil },
		logf:    t.Logf,
	}
	f.srv.containerSID = func(name string) (string, error) {
		f.calls.add("container " + name)

		return aContainerSID, nil
	}
	f.srv.exempt = func(sid string) (func() error, error) {
		if f.exemptErr != nil {
			return nil, f.exemptErr
		}

		f.calls.add("exempt " + sid)

		return func() error {
			f.calls.add("unexempt " + sid)

			return nil
		}, nil
	}
	f.srv.network = func(sid string, n hvservice.Network) (func() error, error) {
		if f.networkErr != nil {
			return nil, f.networkErr
		}

		f.calls.add(fmt.Sprintf("filter %s %v %v", sid, n.Ports, n.Loopback))

		return func() error {
			f.calls.add("unfilter " + sid)

			return nil
		}, nil
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

// aStateDisk is the path the fake peer reads from a state disk handle.
const aStateDisk = `C:\Users\me\.aibox\projects\x\state.ext4`

// fakePeer is the client of a connection, who hands over a state disk
// under a handle; diskErr refuses it.
type fakePeer struct{ f *fixture }

func (p fakePeer) client() (hvservice.Client, error) {
	client := hvservice.Client{User: "S-1-5-21-1-2-3-1001"}
	if p.f.contained {
		client.AppContainer = "S-1-15-2-9-9-9-9-9-9-9"
	}

	return client, nil
}

// aContainerSID is the SID the fake service derives for any container.
const aContainerSID = "S-1-15-2-1-2-3-4-5-6-7"

func (p fakePeer) stateDisk(id string, handle uint64) (stateDisk, error) {
	if p.f.diskErr != nil {
		return stateDisk{}, p.f.diskErr
	}

	p.f.granted = append(p.f.granted, fmt.Sprintf("grant handle %d to %s", handle, id))

	return stateDisk{path: aStateDisk, revoke: func() error {
		p.f.calls.add("revoke")

		return nil
	}}, nil
}

// aConsoleHandle is the handle the fake peer puts the console pipe under
// in the client's process.
const aConsoleHandle = 7

func (p fakePeer) console(name string) (console, error) {
	p.f.pipes.add("make " + name)

	return console{
		give: func() (uint64, error) {
			p.f.pipes.add("give")

			return aConsoleHandle, nil
		},
		close: func() error {
			p.f.pipes.add("close")

			return nil
		},
	}, nil
}

// peer is the fake peer of the fixture's connection.
func (f *fixture) peer() fakePeer {
	return fakePeer{f: f}
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

		f.srv.serve(ctx, server, f.peer())
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

const createLine = `{"op":"create","create":{"memoryMiB":2048,"cpus":2,"cmdline":"console=ttyS0","state":420}}`

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
	assert.Equal(t, uint64(aConsoleHandle), created.Console, "the console pipe, in the client's hands")
	assert.Equal(t, hvservice.Reply{}, started)
	require.Len(t, f.granted, 1)
	assert.True(t, strings.HasPrefix(f.granted[0], "grant handle 420 to "), f.granted[0])
	assert.Equal(t, []string{"create", "start", "terminate", "close", "revoke"}, f.calls.all())
	assert.Zero(t, f.srv.ledger.used, "the memory of the VM is free again")

	require.Len(t, f.docs, 1)
	assert.Equal(t, "console=ttyS0", f.docs[0].VirtualMachine.Chipset.LinuxKernelDirect.KernelCmdLine)
	assert.Equal(t, aStateDisk, f.docs[0].VirtualMachine.Devices.Scsi["0"].Attachments["1"].Path, "the state disk the peer read from the handle")

	pipe := f.docs[0].VirtualMachine.Devices.ComPorts["0"].NamedPipe
	assert.True(t, strings.HasPrefix(pipe, `\\.\pipe\aibox-console-`), pipe)
	assert.Equal(t, []string{"make " + pipe, "give", "close"}, f.pipes.all(), "the pipe of the document was made before the VM and handed over after")
}

func TestAVMThatFailsToBeMadeHandsOverNoConsolePipe(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, func(context.Context, *fixture) (vm, error) { return nil, errors.New("HCS said no") })
	f.serve(t.Context(), t)

	// act
	reply := f.ask(t, createLine)

	// assert
	assert.NotEmpty(t, reply.Error)
	assert.Zero(t, reply.Console)
	require.Len(t, f.pipes.all(), 2)
	assert.Equal(t, "close", f.pipes.all()[1], "the pipe went with the VM")
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
	assert.Equal(t, []string{"create", "revoke"}, f.calls.all(), "the VM's access to the state disk is gone with it")
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
	assert.Equal(t, []string{"create", "terminate", "close", "revoke"}, f.calls.all(), "the VM ended though the service's context had")
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
			want:  []string{"create", "terminate", "close", "revoke"},
		},
		"not started": {
			vm:    fakeVM{startErr: &hcs.Error{Op: "HcsStartComputeSystem", HRESULT: 0x80070005}},
			lines: []string{createLine, `{"op":"start"}`},
			want:  []string{"create", "start", "terminate", "close", "revoke"},
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

func TestAConnectionRefusesAStateDiskThePeerWontHandOver(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, nil)
	f.diskErr = fmt.Errorf("%w: not an open file of aibox", hvservice.ErrBadRequest)
	f.serve(t.Context(), t)

	// act
	reply := f.ask(t, createLine)

	// assert
	assert.Contains(t, reply.Error, "not an open file of aibox")
	assert.Empty(t, f.calls.all(), "no VM was made")
	assert.Zero(t, f.srv.ledger.used)
}

func TestAVMTheHostHasNoMemoryLeftForTakesNoStateDisk(t *testing.T) {
	t.Parallel()

	// arrange: VMs that run hold all the host has
	f := newFixture(t, nil)
	f.srv.ledger.used = 16384
	f.serve(t.Context(), t)

	// act
	reply := f.ask(t, createLine)

	// assert
	assert.Contains(t, reply.Error, "too little memory")
	assert.Equal(t, []string{"revoke"}, f.calls.all(), "the VM's access to the state disk is taken away again")
}

func TestAStopEndsTheVMBeforeItIsAnsweredAndLetsTheClientMakeAnother(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, nil)
	f.serve(t.Context(), t)
	require.Empty(t, f.ask(t, createLine).Error)
	require.Empty(t, f.ask(t, `{"op":"start"}`).Error)

	// act
	stopped := f.ask(t, `{"op":"stop"}`)

	// assert
	assert.Equal(t, hvservice.Reply{}, stopped)
	assert.Equal(t, []string{"create", "start", "terminate", "close", "revoke"}, f.calls.all(), "the VM was gone before the answer")
	assert.Zero(t, f.srv.ledger.used)
	assert.Empty(t, f.ask(t, createLine).Error, "the client may make another")
}

func TestAStopOfNoVMIsRefused(t *testing.T) {
	t.Parallel()

	// arrange
	f := newFixture(t, nil)
	f.serve(t.Context(), t)

	// act
	reply := f.ask(t, `{"op":"stop"}`)

	// assert
	assert.Contains(t, reply.Error, "out of turn")
}

// networkLine is a create that asks for the network of the container.
const networkLine = `{"op":"create","create":{"memoryMiB":2048,"cpus":2,"cmdline":"console=ttyS0","state":420,"network":{"ports":[443,22],"loopback":[63342]}}}`

func TestAVMWithANetworkGetsAContainerWhoseFiltersGoWithIt(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.serve(t.Context(), t)

	// act
	created := f.ask(t, networkLine)
	f.hangUp()

	// assert
	require.Empty(t, created.Error)
	assert.True(t, strings.HasPrefix(created.Container, "aibox."), created.Container)
	assert.Equal(t, aContainerSID, created.ContainerSID)

	calls := f.calls.all()
	require.Len(t, calls, 9)
	assert.Equal(t, "container "+created.Container, calls[0], "named by the service")
	assert.Equal(t, "create", calls[1])
	assert.Equal(t, "filter "+aContainerSID+" [443 22] [63342]", calls[2], "before the reply")
	assert.Equal(t, "exempt "+aContainerSID, calls[3], "the loopback too, as it has a port there")
	assert.Equal(t, []string{"terminate", "close", "revoke", "unexempt " + aContainerSID, "unfilter " + aContainerSID}, calls[4:], "and gone with the connection, after the VM")

	require.Len(t, f.docs, 1)
	bind := f.docs[0].VirtualMachine.Devices.HvSocket.HvSocketConfig.DefaultBindSecurityDescriptor
	assert.Contains(t, bind, "S-1-5-21-1-2-3-1001", "the client listens on the sockets")
	assert.Contains(t, bind, aContainerSID, "and its helper in the container")
}

func TestAVMWithoutANetworkGetsNoContainer(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.serve(t.Context(), t)

	// act
	created := f.ask(t, createLine)
	f.hangUp()

	// assert
	require.Empty(t, created.Error)
	assert.Empty(t, created.Container)
	assert.Empty(t, created.ContainerSID)
	assert.Equal(t, []string{"create", "terminate", "close", "revoke"}, f.calls.all())
}

func TestAVMWhoseNetworkCantBeFilteredIsEnded(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.networkErr = errors.New("the engine said no")
	f.serve(t.Context(), t)

	// act
	created := f.ask(t, networkLine)

	// assert
	assert.NotEmpty(t, created.Error)
	assert.Empty(t, created.Container)
	assert.Contains(t, f.calls.all(), "terminate")
	assert.Zero(t, f.srv.ledger.used, "the memory of the VM is free again")
}

func TestAClientInAContainerGetsNoNetworkForAnother(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.contained = true
	f.serve(t.Context(), t)

	// act
	created := f.ask(t, networkLine)

	// assert
	assert.Contains(t, created.Error, "AppContainer")
	assert.Empty(t, f.calls.all(), "no VM was made")
}

func TestAContainerWithoutLoopbackPortsStaysOffTheLoopback(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.serve(t.Context(), t)

	// act
	created := f.ask(t, `{"op":"create","create":{"memoryMiB":2048,"cpus":2,"cmdline":"console=ttyS0","state":420,"network":{"ports":[443]}}}`)
	f.hangUp()

	// assert
	require.Empty(t, created.Error)

	for _, call := range f.calls.all() {
		assert.NotContains(t, call, "exempt")
	}
}

func TestAVMWhoseContainerCantReachTheLoopbackIsEnded(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.exemptErr = errors.New("the firewall said no")
	f.serve(t.Context(), t)

	// act
	created := f.ask(t, networkLine)

	// assert
	assert.NotEmpty(t, created.Error)
	assert.Contains(t, f.calls.all(), "unfilter "+aContainerSID, "its filters go with it")
	assert.Zero(t, f.srv.ledger.used)
}

func TestTheFiltersOfAContainerOutliveAStopOfItsVM(t *testing.T) {
	// arrange: aibox stops the VM, while the helper in the container may
	// still run
	f := newFixture(t, nil)
	f.serve(t.Context(), t)

	created := f.ask(t, networkLine)
	require.Empty(t, created.Error)

	// act
	stopped := f.ask(t, `{"op":"stop"}`)

	// assert
	require.Empty(t, stopped.Error)
	assert.Contains(t, f.calls.all(), "terminate")
	assert.NotContains(t, f.calls.all(), "unfilter "+aContainerSID, "the helper may still run")

	f.hangUp()
	assert.Contains(t, f.calls.all(), "unfilter "+aContainerSID, "gone with the connection")
}
