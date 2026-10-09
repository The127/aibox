package gitbroker

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/the127/aibox/internal/gitconfig"
)

// sshServer is a git server over SSH for the tests: it runs git
// upload-pack and receive-pack on the bare repositories below root, for
// the user git with the key of the client.
type testSSHServer struct {
	address string
	// hostKeys are the keys the server offers, the first one preferred
	hostKeys []ssh.Signer
	client   ssh.Signer

	mu        sync.Mutex
	logins    int
	protocols []string
	conns     []net.Conn
	// rounds counts the requests of git in the VM that negotiate a fetch:
	// each POST but the one of ls-refs in protocol version 2
	rounds int
}

// drop breaks every connection to the server, as a network that went away.
func (s *testSSHServer) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, conn := range s.conns {
		_ = conn.Close()
	}
}

func newTestSSHServer(t *testing.T, root string, hostKeys ...ssh.Signer) *testSSHServer {
	t.Helper()

	if len(hostKeys) == 0 {
		hostKeys = []ssh.Signer{newEd25519Signer(t)}
	}

	s := &testSSHServer{hostKeys: hostKeys, client: newEd25519Signer(t)}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "git" || string(key.Marshal()) != string(s.client.PublicKey().Marshal()) {
				return nil, assert.AnError
			}

			return &ssh.Permissions{}, nil
		},
	}

	for _, key := range hostKeys {
		config.AddHostKey(key)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	s.address = listener.Addr().String()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()

			go s.serveConn(conn, config, root)
		}
	}()

	return s
}

func (s *testSSHServer) serveConn(conn net.Conn, config *ssh.ServerConfig, root string) {
	_, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()

		return
	}

	s.mu.Lock()
	s.logins++
	s.mu.Unlock()

	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}

		go s.serveSession(channel, requests, root)
	}
}

// serveSession runs the git command of an exec request, with the
// GIT_PROTOCOL of an env request.
func (s *testSSHServer) serveSession(channel ssh.Channel, requests <-chan *ssh.Request, root string) {
	var env []string

	for request := range requests {
		switch request.Type {
		case "env":
			var variable struct{ Name, Value string }
			if ssh.Unmarshal(request.Payload, &variable) == nil && variable.Name == "GIT_PROTOCOL" {
				env = append(env, "GIT_PROTOCOL="+variable.Value)
			}

			_ = request.Reply(true, nil)
		case "exec":
			var command struct{ Command string }
			_ = ssh.Unmarshal(request.Payload, &command)
			service, repository, _ := strings.Cut(command.Command, " ")

			_ = request.Reply(true, nil)

			s.mu.Lock()
			s.protocols = append(s.protocols, strings.Join(env, " "))
			s.mu.Unlock()

			cmd := exec.Command("git", strings.TrimPrefix(service, "git-"), filepath.Join(root, strings.Trim(repository, "'"))) //nolint:gosec // the test's own server
			cmd.Env = append(os.Environ(), env...)
			cmd.Stdout, cmd.Stderr = channel, channel.Stderr()

			// as sshd, the session ends when the command does, whether or
			// not the client is done sending
			stdin, _ := cmd.StdinPipe()
			go func() {
				_, _ = io.Copy(stdin, channel)
				_ = stdin.Close()
			}()

			status := uint32(0)
			if cmd.Run() != nil {
				status = 1
			}

			_, _ = channel.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, status))
			_ = channel.Close()

			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func newEd25519Signer(t *testing.T) ssh.Signer {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)

	return signer
}

func newECDSASigner(t *testing.T) ssh.Signer {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)

	return signer
}

// knownHosts returns the check of host keys aibox makes on the host, which
// knows the keys for example.com, and the algorithms of the keys.
func knownHosts(t *testing.T, keys ...ssh.PublicKey) (ssh.HostKeyCallback, []string) {
	t.Helper()

	var lines []string
	for _, key := range keys {
		lines = append(lines, knownhosts.Line([]string{"example.com"}, key))
	}

	file := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	check, err := knownhosts.New(file)
	require.NoError(t, err)

	algorithms, _ := gitconfig.HostKeyAlgorithms(check, "example.com:22")

	return gitconfig.NewHostKeyCheck(check), algorithms
}

// broker returns a broker that reaches the SSH server for example.com.
func (s *testSSHServer) broker(t *testing.T, remote Remote) (string, *safeWriter) {
	t.Helper()

	log := &safeWriter{w: new(bytes.Buffer)}
	b := newBroker([]Remote{remote}, nil, nil, log)
	b.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "example.com:22" {
			return nil, assert.AnError
		}

		return (&net.Dialer{}).DialContext(ctx, network, s.address)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)

				return
			}

			r.Body = io.NopCloser(bytes.NewReader(body))

			if !bytes.Contains(uncompressed(t, r, body), []byte("command=ls-refs")) {
				s.mu.Lock()
				s.rounds++
				s.mu.Unlock()
			}
		}

		b.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	return server.URL, log
}

// uncompressed is the body of the request as git wrote it.
func uncompressed(t *testing.T, r *http.Request, body []byte) []byte {
	t.Helper()

	if r.Header.Get("Content-Encoding") != "gzip" {
		return body
	}

	reader, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)

	plain, err := io.ReadAll(reader)
	require.NoError(t, err)

	return plain
}

// remote is example.com/owner/repo over SSH with the key of the client.
func (s *testSSHServer) remote(t *testing.T, push ...string) Remote {
	t.Helper()

	hostKey, algorithms := knownHosts(t, s.hostKeys[0].PublicKey())

	return Remote{
		Name:  "example.com/owner/repo",
		Fetch: true,
		Push:  push,
		SSH: &SSH{
			Address: "example.com:22", User: "git", Signers: []ssh.Signer{s.client},
			HostKey: hostKey, HostKeyAlgorithms: algorithms,
		},
	}
}

func TestBrokerClonesOverSSH(t *testing.T) {
	for _, version := range []string{"0", "1", "2"} {
		t.Run("protocol version "+version, func(t *testing.T) {
			// arrange
			s := newServer(t)
			s.repository(t, "owner/repo")
			server := newTestSSHServer(t, s.root)
			brokerURL, _ := server.broker(t, server.remote(t))

			// act
			dir := filepath.Join(t.TempDir(), "clone")
			out, err := git(t, "", "-c", "protocol.version="+version, "clone", brokerURL+"/example.com/owner/repo", dir)

			// assert
			require.NoError(t, err, out)
			assert.Contains(t, gitOK(t, dir, "log", "--format=%s"), "first")
			gitOK(t, dir, "-c", "protocol.version="+version, "fetch", "origin")

			server.mu.Lock()
			defer server.mu.Unlock()

			assert.Equal(t, 1, server.logins, "one login for every request")

			if version != "0" {
				assert.Contains(t, server.protocols, "GIT_PROTOCOL=version="+version)
			}
		})
	}
}

func TestBrokerPushesOverSSH(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	server := newTestSSHServer(t, s.root)
	brokerURL, log := server.broker(t, server.remote(t, "aibox/*"))
	dir := clone(t, brokerURL)

	big := make([]byte, 3<<20)
	_, err := rand.Read(big)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big"), big, 0o600))
	gitOK(t, dir, "add", "big")
	gitOK(t, dir, "commit", "-m", "big")

	// act
	out, err := git(t, dir, "push", "origin", "HEAD:aibox/big")

	// assert
	require.NoError(t, err, out)

	bare := filepath.Join(s.root, "owner/repo.git")
	assert.Contains(t, gitOK(t, bare, "log", "--format=%s", "aibox/big"), "big")
	assert.Contains(t, log.String(), "git push to example.com/owner/repo: refs/heads/aibox/big")

	refused, err := git(t, dir, "push", "origin", "HEAD:main")
	require.Error(t, err)
	assert.Contains(t, refused, "aibox: the config allows pushes only to aibox/*")
	assert.NotContains(t, gitOK(t, bare, "log", "--format=%s", "main"), "big")
}

func TestBrokerAsksForTheHostKeyTypeTheKnownHostsHold(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	known := newEd25519Signer(t)
	server := newTestSSHServer(t, s.root, newECDSASigner(t), known)
	remote := server.remote(t)
	remote.SSH.HostKey, remote.SSH.HostKeyAlgorithms = knownHosts(t, known.PublicKey())
	brokerURL, _ := server.broker(t, remote)

	// act
	out, err := git(t, "", "clone", brokerURL+"/example.com/owner/repo", filepath.Join(t.TempDir(), "clone"))

	// assert
	require.NoError(t, err, out)
}

func TestBrokerOverSSHSaysWhatWentWrong(t *testing.T) {
	cases := map[string]struct {
		change func(t *testing.T, remote *Remote)
		says   string
	}{
		"an unknown host": {
			func(t *testing.T, remote *Remote) { remote.SSH.HostKey, _ = knownHosts(t) },
			"example.com:22: the server is not in the known hosts of this machine",
		},
		"another host key": {
			func(t *testing.T, remote *Remote) {
				remote.SSH.HostKey, _ = knownHosts(t, newEd25519Signer(t).PublicKey())
			},
			"example.com:22: the key of the server is not the one in the known hosts of this machine",
		},
		"another client key": {
			func(t *testing.T, remote *Remote) {
				remote.SSH.Signers = []ssh.Signer{newEd25519Signer(t)}
			},
			"example.com:22 refused the SSH keys of this machine",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			// arrange
			s := newServer(t)
			s.repository(t, "owner/repo")
			server := newTestSSHServer(t, s.root)
			remote := server.remote(t)
			c.change(t, &remote)
			brokerURL, log := server.broker(t, remote)

			// act
			out, err := git(t, "", "clone", brokerURL+"/example.com/owner/repo", filepath.Join(t.TempDir(), "clone"))

			// assert
			require.Error(t, err)
			assert.Contains(t, out, "502")
			assert.Contains(t, log.String(), c.says)
		})
	}
}

func TestBrokerFetchesOverSSHInSeveralRounds(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	server := newTestSSHServer(t, s.root)
	brokerURL, _ := server.broker(t, server.remote(t))

	// commits the server does not have make git negotiate in rounds. Protocol
	// version 2 sends more of them at once, so it needs 64.
	local := clone(t, brokerURL)
	for i := range 64 {
		gitOK(t, local, "commit", "--allow-empty", "-m", fmt.Sprintf("local %d", i))
	}

	for _, version := range []string{"0", "2"} {
		t.Run("protocol version "+version, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "clone")
			require.NoError(t, os.CopyFS(dir, os.DirFS(local)))

			work := clone(t, brokerURL)
			gitOK(t, work, "commit", "--allow-empty", "-m", "remote "+version)
			gitOK(t, work, "push", filepath.Join(s.root, "owner/repo.git"), "HEAD:main")

			server.mu.Lock()
			before := server.rounds
			server.mu.Unlock()

			// act
			out, err := git(t, dir, "-c", "protocol.version="+version, "fetch", "origin")

			// assert
			require.NoError(t, err, out)
			assert.Contains(t, gitOK(t, dir, "log", "--format=%s", "origin/main"), "remote "+version)

			server.mu.Lock()
			defer server.mu.Unlock()

			assert.Greater(t, server.rounds-before, 1, "git negotiated in more than one round")
		})
	}
}

func TestBrokerOverSSHTakesACompressedFetchRequest(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	server := newTestSSHServer(t, s.root)
	brokerURL, _ := server.broker(t, server.remote(t))

	// git compresses the requests of a long negotiation
	var body bytes.Buffer

	compressed := gzip.NewWriter(&body)
	_, err := io.WriteString(compressed, pktLine("command=ls-refs\n")+"0001"+pktLine("ref-prefix refs/heads/\n")+"0000")
	require.NoError(t, err)
	require.NoError(t, compressed.Close())

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, brokerURL+"/example.com/owner/repo/git-upload-pack", &body)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("Git-Protocol", "version=2")

	// act
	response, err := http.DefaultClient.Do(request)

	// assert
	require.NoError(t, err)

	defer func() { _ = response.Body.Close() }()

	answer, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode, string(answer))
	assert.Contains(t, string(answer), "refs/heads/main")
}

func TestBrokerOverSSHPassesOnWhatTheServerSays(t *testing.T) {
	// arrange
	s := newServer(t)
	server := newTestSSHServer(t, s.root)
	brokerURL, log := server.broker(t, server.remote(t))

	// act
	out, err := git(t, "", "clone", brokerURL+"/example.com/owner/repo", filepath.Join(t.TempDir(), "clone"))

	// assert
	require.Error(t, err)
	assert.Contains(t, out, "the server sent no advertisement of git: the server said:")
	assert.Contains(t, log.String(), "does not appear to be a git repository")
}

func TestBrokerLogsInAgainWhenTheConnectionBroke(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	server := newTestSSHServer(t, s.root)
	brokerURL, _ := server.broker(t, server.remote(t))
	clone(t, brokerURL)
	server.drop()

	// act
	out, err := git(t, "", "clone", brokerURL+"/example.com/owner/repo", filepath.Join(t.TempDir(), "again"))

	// assert
	require.NoError(t, err, out)

	server.mu.Lock()
	defer server.mu.Unlock()

	assert.Equal(t, 2, server.logins)
}
