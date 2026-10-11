package gitconfig_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"testing"

	winio "github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"
)

// The agent of the OpenSSH of Windows is on a pipe, not a socket.
func TestSSHKeysForTakeAKeyFromTheAgentOnAPipe(t *testing.T) {
	// arrange
	dir := sshHome(t, "github.com")
	key := writeKey(t, filepath.Join(dir, "id_ed25519"), "secret")

	keyring := agent.NewKeyring()
	require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: key}))

	id := make([]byte, 8)
	_, _ = rand.Read(id)
	pipe := `\\.\pipe\aibox-test-agent-` + hex.EncodeToString(id)

	listener, err := winio.ListenPipe(pipe, nil)
	require.NoError(t, err)
	serveAgentOn(t, listener, keyring)
	// named as Windows takes it too, with slashes and in another case
	t.Setenv("SSH_AUTH_SOCK", "//./PIPE/"+pipe[len(`\\.\pipe\`):])

	keys := loadKeys(t, sshConfig(t, dir, ""))

	// act
	target, err := keys.For(context.Background(), "github.com")

	// assert
	require.NoError(t, err)
	require.Len(t, target.Signers, 1)
	samePublic(t, key, target.Signers[0])
}
