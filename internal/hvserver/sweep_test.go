package hvserver

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSweepEndsEveryVMLeftBehindThoughOneWont(t *testing.T) {
	t.Parallel()

	// arrange
	var ended []string

	list := func(context.Context) ([]string, error) { return []string{"a", "b", "c"}, nil }
	end := func(_ context.Context, id string) error {
		if id == "b" {
			return errors.New("access denied")
		}

		ended = append(ended, id)

		return nil
	}

	// act
	sweep(t.Context(), list, end, t.Logf)

	// assert
	assert.Equal(t, []string{"a", "c"}, ended)
}

func TestSweepEndsNothingItCannotList(t *testing.T) {
	t.Parallel()

	// arrange
	called := false

	list := func(context.Context) ([]string, error) { return nil, errors.New("the service is not there") }
	end := func(context.Context, string) error {
		called = true

		return nil
	}

	// act
	sweep(t.Context(), list, end, t.Logf)

	// assert
	assert.False(t, called)
}
