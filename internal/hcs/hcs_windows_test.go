package hcs_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hcs"
)

func TestTimeoutOfWaitsTheDefaultWithoutADeadline(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint32(120000), hcs.TimeoutOf(t.Context()))
}

func TestTimeoutOfWaitsNotAtAllPastTheDeadline(t *testing.T) {
	t.Parallel()

	// arrange
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	// act
	timeout := hcs.TimeoutOf(ctx)

	// assert
	assert.Equal(t, uint32(0), timeout)
}

func TestTimeoutOfStopsShortOfTheValueForNoLimit(t *testing.T) {
	t.Parallel()

	// arrange
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(100*24*time.Hour))
	defer cancel()

	// act
	timeout := hcs.TimeoutOf(ctx)

	// assert
	assert.Equal(t, uint32(math.MaxUint32-1), timeout)
}

func TestCheckPassesSuccessAndWarnings(t *testing.T) {
	t.Parallel()

	assert.NoError(t, hcs.Check("start", 0, ""))
	assert.NoError(t, hcs.Check("start", 1, ""))
}

func TestCheckFailsWithTheHRESULTAndTheResultDocument(t *testing.T) {
	t.Parallel()

	// act
	err := hcs.Check("start", 0x80370105, `{"Error":-1070137083}`)

	// assert
	var e *hcs.Error

	require.ErrorAs(t, err, &e)
	assert.Equal(t, hcs.Error{Op: "start", HRESULT: 0x80370105, Result: `{"Error":-1070137083}`}, *e)
}

func TestCheckReadsOnlyTheLow32BitsOfTheReturnValue(t *testing.T) {
	t.Parallel()

	assert.NoError(t, hcs.Check("start", 0xFFFFFFFF_00000000, ""))
}

func TestSystemRefusesCallsOnceClosed(t *testing.T) {
	t.Parallel()

	// arrange
	s := &hcs.System{}
	require.NoError(t, s.Close())

	// act
	err := s.Start(t.Context())

	// assert
	assert.ErrorIs(t, err, hcs.ErrClosed)
	assert.NoError(t, s.Close())
}
