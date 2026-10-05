//go:build !linux && !(darwin && cgo)

package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/backend"
)

func TestAHostWithoutABackendSaysSoInsteadOfRunning(t *testing.T) {
	// arrange
	b := newBackend()

	// act
	ran := b.Run(context.Background(), backend.Spec{})

	// assert
	assert.ErrorIs(t, ran, errNoBackend)
}
