//go:build linux

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/vsockns"
)

func TestTheNamespaceHelperIsAHiddenCommand(t *testing.T) {
	// act
	root := newRootCommand(newFixture(t).deps)

	// assert
	var found bool

	for _, command := range root.Commands {
		if command.Name == vsockns.Command {
			found = true

			assert.True(t, command.Hidden)
		}
	}

	require.True(t, found, "no command %q", vsockns.Command)
}
