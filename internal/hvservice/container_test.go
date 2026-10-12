package hvservice_test

import (
	"testing"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hvservice"
)

func TestTheContainerIsNamedAfterItsNumber(t *testing.T) {
	t.Parallel()

	// arrange
	id, err := guid.FromString("5c2ee6b0-5a8f-4b4a-9c55-0c2b5f7e8f10")
	require.NoError(t, err)

	// act
	name := hvservice.ContainerName(id)

	// assert
	assert.Equal(t, "aibox.5c2ee6b0-5a8f-4b4a-9c55-0c2b5f7e8f10", name)
	assert.LessOrEqual(t, len(name), 64, "Windows takes no longer name for a container")
}
