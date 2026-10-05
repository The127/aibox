package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/backend"
)

func TestExitCodeIsTheCodeOfTheCommandInTheVM(t *testing.T) {
	// arrange
	err := fmt.Errorf("run: %w", &backend.ExitError{Code: 7})

	// act
	code := ExitCode(err)

	// assert
	assert.Equal(t, 7, code)
}

func TestExitCodeIsOneForAnyOtherError(t *testing.T) {
	// act
	code := ExitCode(errors.New("no kernel"))

	// assert
	assert.Equal(t, 1, code)
}

func TestExitCodeIsZeroWithoutAnError(t *testing.T) {
	// act
	code := ExitCode(nil)

	// assert
	assert.Equal(t, 0, code)
}
