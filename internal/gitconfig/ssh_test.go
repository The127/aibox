package gitconfig_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/the127/aibox/internal/gitconfig"
)

// sshHome makes a home with an empty ~/.ssh and no agent, whose known
// hosts hold a key for each of the hosts, and returns ~/.ssh.
func sshHome(t *testing.T, hosts ...string) string {
	t.Helper()

	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	var lines string

	for _, host := range hosts {
		lines += knownhosts.Line([]string{host}, newPublicKey(t)) + "\n"
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(lines), 0o600))

	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")

	return dir
}

func newPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	public, err := ssh.NewPublicKey(key.Public())
	require.NoError(t, err)

	return public
}

// writeKey writes a new ed25519 key and its .pub file, with the passphrase
// if there is one, and returns the key.
func writeKey(t *testing.T, file, passphrase string) ed25519.PrivateKey {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte(passphrase))
	}

	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, pem.EncodeToMemory(block), 0o600))

	public, err := ssh.NewPublicKey(key.Public())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file+".pub", ssh.MarshalAuthorizedKey(public), 0o600))

	return key
}

// sshConfig writes an ssh config with the content, and the known hosts in
// the ~/.ssh of the test, so that the test does not depend on the config
// and the home of the person, which ssh takes ~ for in the known hosts. The
// default key files ssh prints with ~, which aibox takes from $HOME.
func sshConfig(t *testing.T, dir, content string) string {
	t.Helper()

	content += "\nHost *\n  UserKnownHostsFile " + filepath.Join(dir, "known_hosts") + "\n  GlobalKnownHostsFile /dev/null\n"

	file := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(file, []byte(content), 0o600))

	return file
}

// runAgent serves an SSH agent with the keys and points SSH_AUTH_SOCK at it.
func runAgent(t *testing.T, keys ...ed25519.PrivateKey) {
	t.Helper()

	t.Setenv("SSH_AUTH_SOCK", serveAgent(t, keys...))
}

// serveAgent serves an SSH agent with the keys and returns its socket.
func serveAgent(t *testing.T, keys ...ed25519.PrivateKey) string {
	t.Helper()

	keyring := agent.NewKeyring()
	for _, key := range keys {
		require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: key}))
	}

	// a short folder, since macOS allows 104 bytes for the path of a socket
	dir, err := os.MkdirTemp("", "agent")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() { _ = agent.ServeAgent(keyring, conn) }()
		}
	}()

	return socket
}

func loadKeys(t *testing.T, config string) *gitconfig.SSHKeys {
	t.Helper()

	keys, err := gitconfig.LoadSSHKeys()
	require.NoError(t, err)
	t.Cleanup(keys.Close)

	keys.Config = config

	return keys
}

func samePublic(t *testing.T, key ed25519.PrivateKey, signer ssh.Signer) {
	t.Helper()

	public, err := ssh.NewPublicKey(key.Public())
	require.NoError(t, err)
	assert.Equal(t, public.Marshal(), signer.PublicKey().Marshal())
}

func TestSSHKeysForTakeADefaultKeyFileWithoutAPassphrase(t *testing.T) {
	// arrange
	dir := sshHome(t, "github.com")
	key := writeKey(t, filepath.Join(dir, "id_ed25519"), "")
	keys := loadKeys(t, sshConfig(t, dir, ""))

	// act
	target, err := keys.For(context.Background(), "github.com")

	// assert
	require.NoError(t, err)
	assert.Equal(t, "github.com:22", target.Address)
	assert.Equal(t, "git", target.User)
	require.Len(t, target.Signers, 1)
	samePublic(t, key, target.Signers[0])
	assert.Equal(t, []string{filepath.Join(dir, "id_ed25519")}, target.Keys)
	assert.Equal(t, []string{ssh.KeyAlgoED25519}, target.HostKeyAlgorithms)
}

func TestSSHKeysForFollowTheSSHConfig(t *testing.T) {
	// arrange
	dir := sshHome(t, "[ssh.github.com]:443")
	writeKey(t, filepath.Join(dir, "id_ed25519"), "")
	work := writeKey(t, filepath.Join(dir, "work"), "")
	runAgent(t, writeKey(t, filepath.Join(t.TempDir(), "other"), ""))
	keys := loadKeys(t, sshConfig(t, dir, "Host github.com\n  HostName ssh.github.com\n  Port 443\n  User someone\n  IdentityFile ~/.ssh/work\n  IdentitiesOnly yes\n"))

	// act
	target, err := keys.For(context.Background(), "github.com")

	// assert
	require.NoError(t, err)
	assert.Equal(t, "ssh.github.com:443", target.Address)
	assert.Equal(t, "git", target.User, "git asks for git@ as git itself does")
	require.Len(t, target.Signers, 1, "only the key the config names, and not the one in the agent")
	samePublic(t, work, target.Signers[0])
}

func TestSSHKeysForTakeAKeyWithAPassphraseFromTheAgent(t *testing.T) {
	// arrange
	dir := sshHome(t, "github.com")
	key := writeKey(t, filepath.Join(dir, "id_ed25519"), "secret")
	runAgent(t, key)
	keys := loadKeys(t, sshConfig(t, dir, ""))

	// act
	target, err := keys.For(context.Background(), "github.com")

	// assert
	require.NoError(t, err)
	require.Len(t, target.Signers, 1, "the agent and the file hold the same key")
	samePublic(t, key, target.Signers[0])
	assert.Equal(t, []string{filepath.Join(dir, "id_ed25519")}, target.Keys)

	signature, err := target.Signers[0].Sign(rand.Reader, []byte("data"))
	require.NoError(t, err)
	require.NoError(t, target.Signers[0].PublicKey().Verify([]byte("data"), signature))
}

func TestSSHKeysForSayWhatIsMissing(t *testing.T) {
	t.Run("a key", func(t *testing.T) {
		// arrange
		dir := sshHome(t, "github.com")
		writeKey(t, filepath.Join(dir, "id_ed25519"), "secret")
		keys := loadKeys(t, sshConfig(t, dir, ""))

		// act
		_, err := keys.For(context.Background(), "github.com")

		// assert
		require.ErrorIs(t, err, gitconfig.ErrNoSSHKey)
	})

	t.Run("the host in the known hosts", func(t *testing.T) {
		// arrange
		dir := sshHome(t, "github.com")
		writeKey(t, filepath.Join(dir, "id_ed25519"), "")
		keys := loadKeys(t, sshConfig(t, dir, ""))

		// act
		_, err := keys.For(context.Background(), "gitlab.com")

		// assert
		require.ErrorIs(t, err, gitconfig.ErrUnknownHost)
	})
}

func TestHostKeyCheckSaysWhatIsWrongWithTheKeyOfAServer(t *testing.T) {
	// arrange
	known := newPublicKey(t)
	file := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(file, []byte(knownhosts.Line([]string{"github.com"}, known)+"\n"), 0o600))
	check, err := knownhosts.New(file)
	require.NoError(t, err)

	hostKey := gitconfig.NewHostKeyCheck(check)

	// act and assert
	require.NoError(t, hostKey("github.com:22", &net.TCPAddr{}, known))
	require.ErrorContains(t, hostKey("github.com:22", &net.TCPAddr{}, newPublicKey(t)), "the key of the server is not the one in the known hosts of this machine")
	require.ErrorIs(t, hostKey("gitlab.com:22", &net.TCPAddr{}, known), gitconfig.ErrUnknownHost)
}

func TestHostKeyAlgorithmsTakeSHA2ForAnRSAKey(t *testing.T) {
	// arrange
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	public, err := ssh.NewPublicKey(&key.PublicKey)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(file, []byte(knownhosts.Line([]string{"github.com"}, public)+"\n"), 0o600))
	check, err := knownhosts.New(file)
	require.NoError(t, err)

	// act
	algorithms, err := gitconfig.HostKeyAlgorithms(check, "github.com:22")

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}, algorithms)
}

func TestSSHKeysForFollowTheAgentAndTheKnownHostsOfTheSSHConfig(t *testing.T) {
	// arrange
	dir := sshHome(t)
	key := writeKey(t, filepath.Join(t.TempDir(), "in-the-agent"), "")
	socket := serveAgent(t, key)

	known := filepath.Join(t.TempDir(), "known")
	require.NoError(t, os.WriteFile(known, []byte(knownhosts.Line([]string{"the-alias"}, newPublicKey(t))+"\n"), 0o600))

	keys := loadKeys(t, sshConfig(t, dir, "Host github.com\n  IdentityAgent "+socket+"\n  UserKnownHostsFile "+known+"\n  HostKeyAlias the-alias\n"))

	// act
	target, err := keys.For(context.Background(), "github.com")

	// assert
	require.NoError(t, err)
	assert.Equal(t, "github.com:22", target.Address)
	require.Len(t, target.Signers, 1)
	samePublic(t, key, target.Signers[0])
	require.ErrorContains(t, target.HostKey("github.com:22", &net.TCPAddr{}, newPublicKey(t)), "the-alias:22: the key of the server is not the one")
}

func TestSSHKeysForRefuseAHostBehindAProxy(t *testing.T) {
	for _, directive := range []string{"ProxyJump jump.example.com", "ProxyCommand nc %h %p"} {
		t.Run(directive, func(t *testing.T) {
			// arrange
			dir := sshHome(t, "github.com")
			writeKey(t, filepath.Join(dir, "id_ed25519"), "")
			keys := loadKeys(t, sshConfig(t, dir, "Host github.com\n  "+directive+"\n"))

			// act
			_, err := keys.For(context.Background(), "github.com")

			// assert
			require.ErrorIs(t, err, gitconfig.ErrProxy)
		})
	}
}
