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
		{Name: "ssh", URL: "ssh://git@github.com/owner/repo.git"},
		{Name: "plain", URL: "http://gitea.local:3000/owner/repo"},
		{Name: "port", URL: "https://github.com:8443/o/r"},
		{Name: "v6", URL: "http://[::1]:3000/o/r"},
		{Name: "sshport", URL: "ssh://git@github.com:22/o/r"},
		{Name: "nouser", URL: "ssh://host:22/path"},
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
		{Name: "origin", URL: "https://x-access-token:ghp_secret@github.com/o/r"}, //nolint:gosec // a made-up token the check must refuse
		{Name: "origin", URL: "https://user@github.com/o/r"},
		{Name: "origin", URL: "file:///home/me/repo"},
		{Name: "origin", URL: "/home/me/repo"},
		{Name: "origin", URL: "../repo"},
		{Name: "origin", URL: "ext::sh -c touch% /tmp/x"},
		{Name: "origin", URL: "https://github.com"},
		{Name: "origin", URL: "ssh://git:password@github.com/o/r"}, //nolint:gosec // a made-up password the check must refuse
		{Name: "origin", URL: "ssh://-oProxyCommand=x/p"},
		{Name: "origin", URL: "ssh://git@-oProxy/x"},
		{Name: "origin", URL: "-oFoo@host:x"},
		{Name: "origin", URL: "x@-oProxyCommand=y:path"},
		{Name: "origin", URL: "https://-h/o/r"},
		{Name: "origin", URL: "https://github.com/o/r?token=abc"},
		{Name: "origin", URL: "https://github.com/o/r#abc"},
	} {
		// act
		err := task.Clone{Base: cloneBase, Remotes: []task.Remote{remote}}.Check()

		// assert
		assert.ErrorIs(t, err, task.ErrBadSettings, remote)
	}
}
