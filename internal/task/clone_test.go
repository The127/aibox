package task_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/task"
)

const cloneBase = "0123456789abcdef0123456789abcdef01234567"

func TestACloneWithACommitAndRemotesPassesTheCheck(t *testing.T) {
	// arrange
	clone := task.Clone{Base: cloneBase, Remotes: []task.Remote{
		{Name: "origin", URL: "https://github.com/owner/repo.git"},
		{Name: "up_stream-2", URL: "git@github.com:owner/repo.git"},
	}}

	// act
	err := clone.Check()

	// assert
	assert.NoError(t, err)
}

func TestACloneNeedsAFullCommitAsItsBase(t *testing.T) {
	for _, base := range []string{"", "HEAD", "0123456", "--upload-pack=x", strings.ToUpper(cloneBase)} {
		// act
		err := task.Clone{Base: base}.Check()

		// assert
		assert.ErrorIs(t, err, task.ErrBadSettings, base)
	}
}

func TestACloneRefusesRemotesGitCouldMisread(t *testing.T) {
	for _, remote := range []task.Remote{
		{Name: "-origin", URL: "https://example.com/r"},
		{Name: ".hidden", URL: "https://example.com/r"},
		{Name: "a..b", URL: "https://example.com/r"},
		{Name: "a/b", URL: "https://example.com/r"},
		{Name: "origin.lock", URL: "https://example.com/r"},
		{Name: "", URL: "https://example.com/r"},
		{Name: "origin", URL: ""},
		{Name: "origin", URL: "--upload-pack=touch /tmp/x"},
		{Name: "origin", URL: "https://example.com/r\nx"},
		{Name: "origin", URL: "https://example.com/a b"},
	} {
		// act
		err := task.Clone{Base: cloneBase, Remotes: []task.Remote{remote}}.Check()

		// assert
		assert.ErrorIs(t, err, task.ErrBadSettings, remote)
	}
}
