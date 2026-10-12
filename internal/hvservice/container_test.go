package hvservice_test

import (
	"strings"
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

func TestOnlyANameOfTheServiceIsAContainerName(t *testing.T) {
	t.Parallel()

	id, err := guid.FromString("5c2ee6b0-5a8f-4b4a-9c55-0c2b5f7e8f10")
	require.NoError(t, err)

	tests := map[string]struct {
		name string
		want bool
	}{
		"of the service":         {name: hvservice.ContainerName(id), want: true},
		"in upper case":          {name: strings.ToUpper(hvservice.ContainerName(id)), want: true},
		"of a test":              {name: "aibox.test.vtxk4r74vaqsmws53bvfdxhhza"},
		"of an app":              {name: "microsoft.windowscalculator_8wekyb3d8bbwe"},
		"a number with braces":   {name: "aibox.{5c2ee6b0-5a8f-4b4a-9c55-0c2b5f7e8f10}"},
		"a number with a suffix": {name: hvservice.ContainerName(id) + ".old"},
		"the prefix alone":       {name: "aibox."},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, hvservice.IsContainerName(tt.name))
		})
	}
}
