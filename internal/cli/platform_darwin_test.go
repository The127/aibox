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
	vz, ok := b.(vzlaunch.Backend)
	assert.True(t, ok)
	assert.NotNil(t, vz.Confine, "aibox would run unconfined")
	assert.Empty(t, platformCommands())
}
