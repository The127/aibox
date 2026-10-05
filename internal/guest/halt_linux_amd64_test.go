package guest_test

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/guest"
)

func TestHaltResetsTheMachineOnX86(t *testing.T) {
	assert.Equal(t, syscall.LINUX_REBOOT_CMD_RESTART, guest.HaltCommand)
}
