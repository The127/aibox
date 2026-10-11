package hvservice_test

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hvservice"
)

func TestMessagesGoOverAStreamOneLineEach(t *testing.T) {
	t.Parallel()

	// arrange
	var stream bytes.Buffer

	create := hvservice.Request{Op: hvservice.OpCreate, Create: &hvservice.Create{
		MemoryMiB: 2048,
		CPUs:      2,
		Cmdline:   "console=ttyS0 aibox.hyperv",
		State:     0x1a4,
	}}
	start := hvservice.Request{Op: hvservice.OpStart}

	// act
	require.NoError(t, hvservice.Write(&stream, create))
	require.NoError(t, hvservice.Write(&stream, start))

	lines := strings.Count(stream.String(), "\n")
	r := hvservice.NewReader(&stream)

	var first, second hvservice.Request

	require.NoError(t, hvservice.Read(r, &first))
	require.NoError(t, hvservice.Read(r, &second))

	// assert
	assert.Equal(t, create, first)
	assert.Equal(t, start, second)
	assert.Equal(t, 2, lines)
}

func TestTheLongestRequestGoesThroughWhole(t *testing.T) {
	t.Parallel()

	// arrange
	var stream bytes.Buffer

	// every byte of the command line is one JSON writes as \u00XX
	longest := hvservice.Request{Op: hvservice.OpCreate, Create: &hvservice.Create{
		MemoryMiB: math.MinInt,
		CPUs:      math.MinInt,
		Cmdline:   strings.Repeat("\x01", hvservice.MaxCmdline),
		State:     math.MaxUint64,
	}}

	// act
	writeErr := hvservice.Write(&stream, longest)

	var request hvservice.Request
	readErr := hvservice.Read(hvservice.NewReader(&stream), &request)

	// assert
	require.NoError(t, writeErr)
	require.NoError(t, readErr)
	assert.Equal(t, longest, request)
}

// replyOf is a reply whose line, newline included, is size bytes long:
// {"error":"..."} and the newline are 13 bytes besides the error.
func replyOf(size int) hvservice.Reply {
	return hvservice.Reply{Error: strings.Repeat("x", size-13)}
}

func TestTheLongestMessageWriteSendsIsTheLongestReadTakes(t *testing.T) {
	t.Parallel()

	// arrange
	var stream bytes.Buffer

	// act
	writeErr := hvservice.Write(&stream, replyOf(hvservice.MaxMessage))
	tooLong := hvservice.Write(io.Discard, replyOf(hvservice.MaxMessage+1))

	var reply hvservice.Reply
	readErr := hvservice.Read(hvservice.NewReader(&stream), &reply)

	// assert
	require.NoError(t, writeErr)
	require.NoError(t, readErr)
	assert.Equal(t, replyOf(hvservice.MaxMessage), reply)
	assert.ErrorIs(t, tooLong, hvservice.ErrMessageTooLong)
}

func TestReadRefusesALineOneByteLongerThanTheLimit(t *testing.T) {
	t.Parallel()

	// arrange
	line := `{"error":"` + strings.Repeat("x", hvservice.MaxMessage-13+1) + "\"}\n"

	// act
	var reply hvservice.Reply
	err := hvservice.Read(hvservice.NewReader(strings.NewReader(line)), &reply)

	// assert
	assert.ErrorIs(t, err, hvservice.ErrMessageTooLong)
}

func TestReadFailsOnALineCutShortByTheEndOfTheStream(t *testing.T) {
	t.Parallel()

	// act
	var request hvservice.Request
	err := hvservice.Read(hvservice.NewReader(strings.NewReader(`{"op":"start"}`)), &request)

	// assert
	assert.ErrorIs(t, err, io.EOF)
}

func TestReadTellsALineThatIsNoMessageAndGoesOn(t *testing.T) {
	t.Parallel()

	// arrange
	r := hvservice.NewReader(strings.NewReader("{\"op\":\n{\"op\":\"start\"}\n"))

	// act
	var bad, next hvservice.Request
	badErr := hvservice.Read(r, &bad)
	nextErr := hvservice.Read(r, &next)

	// assert
	assert.ErrorIs(t, badErr, hvservice.ErrBadMessage)
	require.NoError(t, nextErr)
	assert.Equal(t, hvservice.OpStart, next.Op)
}

func TestReadFailsAtTheEndOfTheStream(t *testing.T) {
	t.Parallel()

	// act
	var request hvservice.Request
	err := hvservice.Read(hvservice.NewReader(strings.NewReader("")), &request)

	// assert
	assert.ErrorIs(t, err, io.EOF)
}

func TestRefusalKeepsAShortReasonWhole(t *testing.T) {
	t.Parallel()

	// act
	reply := hvservice.Refusal(errors.New("the VM did not start"))

	// assert
	assert.Equal(t, "the VM did not start", reply.Error)
}

func TestRefusalCutsALongReasonToFitAMessage(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"plain":            strings.Repeat("x", 2*hvservice.MaxMessage),
		"all escaped":      strings.Repeat("\x01", 2*hvservice.MaxMessage),
		"runes of 3 bytes": strings.Repeat("ä€", hvservice.MaxMessage),
		"escaped runes":    strings.Repeat("< ", hvservice.MaxMessage),
		"not UTF-8":        strings.Repeat("\x80x", 2*hvservice.MaxMessage),
	}

	for name, reason := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			var stream bytes.Buffer

			// act
			reply := hvservice.Refusal(errors.New(reason))
			writeErr := hvservice.Write(&stream, reply)

			// assert
			require.NoError(t, writeErr)
			assert.True(t, strings.HasPrefix(strings.ToValidUTF8(reason, "�"), reply.Error))
			assert.True(t, utf8.ValidString(reply.Error))
			assert.NotEmpty(t, reply.Error)
		})
	}
}
