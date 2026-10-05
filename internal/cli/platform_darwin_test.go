package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/applelaunch"
)

func TestMacOSRunsTheVMWithAppleContainerTool(t *testing.T) {
	// act
	b := newBackend()

	// assert
	assert.Equal(t, applelaunch.NewBackend(), b)
	assert.Empty(t, platformCommands())
}
