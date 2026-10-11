package hvservice_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/hvservice"
)

func TestSIDsOfAClientInAnAppContainerHaveTheAppContainersToo(t *testing.T) {
	t.Parallel()

	// act
	sids := hvservice.Client{User: "S-1-5-21-1-2-3-1001", AppContainer: "S-1-15-2-1-2"}.SIDs()

	// assert
	assert.Equal(t, []string{"S-1-5-21-1-2-3-1001", "S-1-15-2-1-2"}, sids)
}
