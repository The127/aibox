package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

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

// mistype runs aibox with args and returns what it printed and the error,
// without ending the test process on the exit code of the error.
func mistype(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	root := newRootCommand(newFixture(t).deps)
	root.Writer, root.ErrWriter = &out, &out
	root.ExitErrHandler = func(context.Context, *cli.Command, error) {}

	err := root.Run(context.Background(), append([]string{"aibox"}, args...))

	return out.String(), err
}

func TestMistypedCommandNamesTheClosestOne(t *testing.T) {
	// act
	_, err := mistype(t, "rnu")

	// assert
	assert.EqualError(t, err, `No help topic for 'rnu'. Did you mean "run"?`)
}

func TestMistypedSubcommandNamesTheClosestOne(t *testing.T) {
	// act
	_, err := mistype(t, "config", "sugest")

	// assert
	assert.EqualError(t, err, `No help topic for 'sugest'. Did you mean "suggest"?`)
}

func TestMistypedFlagNamesTheClosestOne(t *testing.T) {
	// act
	out, err := mistype(t, "--verson")

	// assert
	require.Error(t, err)
	assert.Contains(t, out, `Did you mean "--version"?`)
}

func TestMistypedFlagOfASubcommandNamesTheClosestOne(t *testing.T) {
	// act
	out, err := mistype(t, "run", "--sehll")

	// assert
	require.Error(t, err)
	assert.Contains(t, out, `Did you mean "--shell"?`)
}

func TestEveryCommandSuggests(t *testing.T) {
	// arrange
	root := newRootCommand(newFixture(t).deps)

	// act
	var without []string

	var walk func(cmd *cli.Command)
	walk = func(cmd *cli.Command) {
		if !cmd.Suggest {
			without = append(without, cmd.FullName())
		}

		for _, sub := range cmd.Commands {
			walk(sub)
		}
	}
	walk(root)

	// assert
	assert.Empty(t, without)
}
