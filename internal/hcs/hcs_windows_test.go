package hcs_test

import (
	"context"
	"encoding/json"
	"errors"
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

// deadlineIn is a context whose deadline is always the same time away from
// the moment it is asked for, so that a test of the rounding does not
// depend on how fast it runs.
type deadlineIn struct {
	context.Context

	d time.Duration
}

func (c deadlineIn) Deadline() (time.Time, bool) {
	return time.Now().Add(c.d), true
}

func TestTimeoutOfRoundsUpToTheNextMillisecond(t *testing.T) {
	t.Parallel()

	// arrange
	ctx := deadlineIn{t.Context(), time.Millisecond + 500*time.Microsecond}

	// act
	timeout := hcs.TimeoutOf(ctx)

	// assert
	assert.Equal(t, uint32(2), timeout)
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

func TestSummaryReadsAListWithSystemsOfOtherPrograms(t *testing.T) {
	t.Parallel()

	// arrange
	doc := `[{"Id":"275ad416-38f5-4fce-8daa-64d3588d9078","SystemType":"VirtualMachine",` +
		`"Owner":"aibox-scratch","RuntimeId":"275ad416-38f5-4fce-8daa-64d3588d9078"},` +
		`{"Id":"other","SystemType":"Container","Owner":"someone","RuntimeId":""}]`

	// act
	var systems []hcs.Summary
	err := json.Unmarshal([]byte(doc), &systems)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []hcs.Summary{
		{ID: "275ad416-38f5-4fce-8daa-64d3588d9078", Owner: "aibox-scratch"},
		{ID: "other", Owner: "someone"},
	}, systems)
}

func TestPropertiesReadTheStateBesideTheSummary(t *testing.T) {
	t.Parallel()

	// act
	var p hcs.Properties
	err := json.Unmarshal([]byte(`{"Id":"a","Owner":"aibox","RuntimeId":"275ad416-38f5-4fce-8daa-64d3588d9078","State":"Running"}`), &p)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "a", p.ID)
	assert.Equal(t, "Running", p.State)
	assert.Equal(t, "275ad416-38f5-4fce-8daa-64d3588d9078", p.RuntimeID.String())
}

func TestOpenFailsWithTheCauseOfAnEndedContext(t *testing.T) {
	t.Parallel()

	// arrange
	cause := errors.New("the client went away")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)

	// act
	_, err := hcs.Open(ctx, "00000000-0000-0000-0000-000000000000")

	// assert
	assert.ErrorIs(t, err, cause)
}

func TestSystemWithoutAHandleRefusesCalls(t *testing.T) {
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
