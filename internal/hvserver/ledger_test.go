package hvserver

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLedgerTakesVMsUpToAllTheHostHas(t *testing.T) {
	t.Parallel()

	// arrange
	var l ledger

	_, err := l.reserve(6000, 16000)
	require.NoError(t, err)

	// act
	_, err = l.reserve(10000, 16000)

	// assert
	assert.NoError(t, err)
}

func TestLedgerRefusesAVMBeyondWhatTheHostHasLeft(t *testing.T) {
	t.Parallel()

	// arrange
	var l ledger

	_, err := l.reserve(6000, 16000)
	require.NoError(t, err)

	// act
	_, err = l.reserve(10001, 16000)

	// assert
	assert.ErrorIs(t, err, ErrNoMemory)
}

func TestReleaseGivesTheMemoryBackOnce(t *testing.T) {
	t.Parallel()

	// arrange
	var l ledger

	release, err := l.reserve(10000, 16000)
	require.NoError(t, err)

	_, err = l.reserve(6000, 16000)
	require.NoError(t, err)

	// act
	release()
	release()

	// assert
	_, err = l.reserve(10000, 16000)
	require.NoError(t, err, "the memory of the first VM is free again")

	_, err = l.reserve(1, 16000)
	assert.ErrorIs(t, err, ErrNoMemory, "the second release gave back nothing")
}

func TestLedgerKeepsCountWhenVMsComeAndGoAtOnce(t *testing.T) {
	t.Parallel()

	// arrange: 200 VMs of 100 MiB at once on a host of 5000, where 50 fit;
	// each holds its memory until all have asked
	var (
		l         ledger
		asked, wg sync.WaitGroup
		taken     atomic.Int32
		hold      = make(chan struct{})
	)

	asked.Add(200)

	// act
	for range 200 {
		wg.Go(func() {
			release, err := l.reserve(100, 5000)
			asked.Done()

			if err != nil {
				return
			}

			taken.Add(1)
			<-hold
			release()
		})
	}

	asked.Wait()
	used := l.used
	close(hold)
	wg.Wait()

	// assert
	assert.Equal(t, int32(50), taken.Load(), "as many VMs as fit, and no more")
	assert.Equal(t, 5000, used)
	assert.Zero(t, l.used, "every VM gave its memory back")
}

func TestARefusedVMTakesNothing(t *testing.T) {
	t.Parallel()

	// arrange
	var l ledger

	_, err := l.reserve(6000, 16000)
	require.NoError(t, err)

	_, err = l.reserve(10001, 16000)
	require.ErrorIs(t, err, ErrNoMemory)

	// act
	_, err = l.reserve(10000, 16000)

	// assert
	assert.NoError(t, err)
}

func TestLedgerRefusesAVMOfNoMemory(t *testing.T) {
	t.Parallel()

	for _, mib := range []int{0, -1} {
		// arrange
		var l ledger

		// act
		_, err := l.reserve(mib, 16000)

		// assert
		require.ErrorIs(t, err, errNoVM, "%d MiB", mib)
		assert.Zero(t, l.used)
	}
}
