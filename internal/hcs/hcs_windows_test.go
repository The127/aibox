package hcs_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hcs"
)

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
