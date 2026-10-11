package hvserver

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostHasTheMemoryAndTheProcessorsOfTheMachine(t *testing.T) {
	t.Parallel()

	// act
	h, err := host()

	// assert
	require.NoError(t, err)
	assert.Positive(t, h.MemoryMiB)
	// Go may be kept to fewer processors than the machine has, never more
	assert.GreaterOrEqual(t, h.CPUs, runtime.NumCPU())
}
