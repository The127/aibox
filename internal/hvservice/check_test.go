package hvservice_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/hvservice"
)

func TestCheckTakesACreateWithItsVMAndABareStart(t *testing.T) {
	t.Parallel()

	for _, request := range []hvservice.Request{
		{Op: hvservice.OpCreate, Create: &hvservice.Create{}},
		{Op: hvservice.OpStart},
	} {
		t.Run(string(request.Op), func(t *testing.T) {
			t.Parallel()

			// act
			err := request.Check()

			// assert
			assert.NoError(t, err)
		})
	}
}

func TestCheckRefusesARequestTheServiceWouldNotDoAsMeant(t *testing.T) {
	t.Parallel()

	tests := map[string]hvservice.Request{
		"no op":                 {},
		"an unknown op":         {Op: "delete"},
		"create without its VM": {Op: hvservice.OpCreate},
		"start with a VM":       {Op: hvservice.OpStart, Create: &hvservice.Create{}},
	}

	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			err := request.Check()

			// assert
			assert.ErrorIs(t, err, hvservice.ErrBadRequest)
		})
	}
}
