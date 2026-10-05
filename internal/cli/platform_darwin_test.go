//go:build darwin && cgo

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/vzlaunch"
)

func TestMacOSRunsTheVMWithVirtualizationFramework(t *testing.T) {
	// act
	b := newBackend()

	// assert
	assert.Equal(t, vzlaunch.NewBackend(), b)
	assert.Empty(t, platformCommands())
}
