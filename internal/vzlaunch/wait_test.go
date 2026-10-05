//go:build darwin && cgo

package vzlaunch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVM is a VM that changes state as the test says, and stops when told.
type fakeVM struct {
	mu     sync.Mutex
	state  vz.VirtualMachineState
	states chan vz.VirtualMachineState
	stops  int
}

func newFakeVM() *fakeVM {
	return &fakeVM{state: vz.VirtualMachineStateRunning, states: make(chan vz.VirtualMachineState, 8)}
}

func (v *fakeVM) set(state vz.VirtualMachineState) {
	v.mu.Lock()
	v.state = state
	v.mu.Unlock()
	v.states <- state
}

func (v *fakeVM) State() vz.VirtualMachineState {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.state
}

func (v *fakeVM) CanStop() bool { return v.State() == vz.VirtualMachineStateRunning }

func (v *fakeVM) Stop() error {
	v.mu.Lock()
	v.stops++
	v.mu.Unlock()

	go func() {
		v.set(vz.VirtualMachineStateStopping)
		v.set(vz.VirtualMachineStateStopped)
	}()

	return nil
}

func (v *fakeVM) StateChangedNotify() <-chan vz.VirtualMachineState { return v.states }

func (v *fakeVM) stopCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.stops
}

func terminal() *watched {
	return &watched{connected: make(chan struct{}), ended: make(chan struct{}), again: make(chan struct{})}
}

func quick() Backend {
	return Backend{BootTimeout: time.Minute, SessionEndDelay: time.Second, PowerOffWait: time.Minute}
}

func TestWaitEndsWhenTheVMStopsByItself(t *testing.T) {
	// arrange
	v := newFakeVM()
	v.set(vz.VirtualMachineStateStopped)

	// act
	err := quick().wait(context.Background(), v, terminal())

	// assert
	require.NoError(t, err)
	assert.Zero(t, v.stopCount())
}

func TestWaitStopsAGuestThatNeverConnects(t *testing.T) {
	// arrange
	v := newFakeVM()
	b := quick()
	b.BootTimeout = 50 * time.Millisecond

	// act
	err := b.wait(context.Background(), v, terminal())

	// assert
	require.ErrorIs(t, err, ErrNoBoot)
	assert.Equal(t, 1, v.stopCount())
}

func TestWaitLetsAConnectedGuestTakeLongerThanTheBoot(t *testing.T) {
	// arrange
	v := newFakeVM()
	b := quick()
	b.BootTimeout = 50 * time.Millisecond
	w := terminal()
	close(w.connected)

	time.AfterFunc(200*time.Millisecond, func() { v.set(vz.VirtualMachineStateStopped) })

	// act
	err := b.wait(context.Background(), v, w)

	// assert
	require.NoError(t, err)
	assert.Zero(t, v.stopCount())
}

func TestWaitStopsTheVMWhenTheContextEnds(t *testing.T) {
	// arrange
	v := newFakeVM()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// act
	err := quick().wait(ctx, v, terminal())

	// assert
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, v.stopCount())
}

func TestWaitStopsAVMThatStaysUpAfterItsSession(t *testing.T) {
	// arrange
	v := newFakeVM()
	b := quick()
	b.PowerOffWait = 50 * time.Millisecond
	w := terminal()
	close(w.connected)
	close(w.ended)

	// act
	err := b.wait(context.Background(), v, w)

	// assert
	require.NoError(t, err)
	assert.Equal(t, 1, v.stopCount())
}

func TestAVMThatStoppedByItselfIsNoErrorWhenATimerComesFirst(t *testing.T) {
	// arrange
	v := newFakeVM()
	b := quick()
	b.PowerOffWait = time.Millisecond
	w := terminal()
	close(w.connected)
	close(w.ended)

	// the VM stopped, but wait sees the timer before the state
	v.mu.Lock()
	v.state = vz.VirtualMachineStateStopped
	v.mu.Unlock()

	// act
	err := b.wait(context.Background(), v, w)

	// assert
	assert.NoError(t, err)
}

func TestWaitReportsAVMThatFailed(t *testing.T) {
	// arrange
	v := newFakeVM()
	v.set(vz.VirtualMachineStateError)

	// act
	err := quick().wait(context.Background(), v, terminal())

	// assert
	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrNoBoot))
}

func TestWaitStopsAGuestThatStartedOver(t *testing.T) {
	// arrange
	v := newFakeVM()
	w := terminal()
	close(w.connected)
	close(w.again)

	// act
	err := quick().wait(context.Background(), v, w)

	// assert
	require.ErrorIs(t, err, ErrStartedOver)
	assert.Equal(t, 1, v.stopCount())
}

func TestTheKernelRebootsAfterAPanicAndHearsTheWordsOfTheGuest(t *testing.T) {
	// act
	line := cmdline([]string{"aibox.shell", "aibox.terminal=1024"})

	// assert
	assert.Contains(t, strings.Fields(line), "panic=1")
	assert.True(t, strings.HasSuffix(line, " aibox.shell aibox.terminal=1024"), line)
}
