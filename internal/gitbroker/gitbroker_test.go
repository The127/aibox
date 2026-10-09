package gitbroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi" //nolint:gosec // serves git http-backend to the tests, on a Go without httpoxy
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var login = Login{Username: "host-user", Password: "host-token"}

// gitTimeout bounds each git the tests run, so that a broker that stops
// answering fails the test instead of hanging it.
const gitTimeout = 30 * time.Second

// gitExecPath is the folder of the programs of git, such as
// git-http-backend.
var gitExecPath = sync.OnceValues(func() (string, error) {
	out, err := exec.Command("git", "--exec-path").Output()

	return strings.TrimSpace(string(out)), err
})

// template is a bare repository with a commit on main, which repository
// copies, since making one with git takes several git processes.
var template struct {
	once sync.Once
	dir  string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()

	if template.dir != "" {
		_ = os.RemoveAll(template.dir)
	}

	os.Exit(code)
}

// server is a git server for the tests: git http-backend over the bare
// repositories below root, which wants the login of the host.
type server struct {
	root string
	url  string

	mu      sync.Mutex
	headers []http.Header
}

func newServer(t *testing.T) *server {
	t.Helper()

	execPath, err := gitExecPath()
	require.NoError(t, err)

	s := &server{root: t.TempDir()}
	backend := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + s.root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=host-user"},
	}

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()

		if user, password, ok := r.BasicAuth(); !ok || user != login.Username || password != login.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "who are you", http.StatusUnauthorized)

			return
		}

		// net/http/cgi refuses a chunked body, which git sends for a large
		// push and a real server takes
		if r.ContentLength < 0 {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)

				return
			}

			r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
			r.TransferEncoding = nil
		}

		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)

	s.url = httpServer.URL

	return s
}

// repository makes a bare repository on the server with a commit on main.
func (s *server) repository(t *testing.T, name string) {
	t.Helper()

	template.once.Do(func() { template.dir, template.err = makeTemplate() })
	require.NoError(t, template.err)

	require.NoError(t, os.CopyFS(filepath.Join(s.root, name+".git"), os.DirFS(filepath.Join(template.dir, "bare"))))
}

// makeTemplate makes the folder of the repository that repository copies,
// in bare below it.
func makeTemplate() (string, error) {
	dir, err := os.MkdirTemp("", "gitbroker-template")
	if err != nil {
		return "", err
	}

	bare, work, home := filepath.Join(dir, "bare"), filepath.Join(dir, "work"), filepath.Join(dir, "home")

	for _, step := range []struct {
		dir  string
		args []string
	}{
		{"", []string{"init", "--bare", "--initial-branch=main", bare}},
		{bare, []string{"config", "http.receivepack", "true"}},
		{"", []string{"init", "--initial-branch=main", work}},
		{work, []string{"commit", "--allow-empty", "-m", "first"}},
		{work, []string{"push", bare, "main"}},
	} {
		if out, err := gitIn(context.Background(), home, step.dir, step.args...); err != nil {
			return dir, fmt.Errorf("git %s: %w: %s", strings.Join(step.args, " "), err, out)
		}
	}

	return dir, nil
}

// broker returns a broker in front of the server for the remotes.
func (s *server) broker(t *testing.T, remotes ...Remote) (string, *bytes.Buffer) {
	t.Helper()

	upstream := func(name string) *url.URL {
		u, err := url.Parse(s.url)
		require.NoError(t, err)

		_, repository, _ := strings.Cut(name, "/")
		u.Path = "/" + repository + ".git"

		return u
	}

	var log bytes.Buffer

	b := httptest.NewServer(newBroker(remotes, upstream, http.DefaultTransport, &safeWriter{w: &log}))
	t.Cleanup(b.Close)

	return b.URL, &log
}

type safeWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *safeWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.w.String()
}

func (s *safeWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.w.Write(p)
}

// git runs git in dir with no config of the person, as git in the VM would
// run without a login.
func git(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), gitTimeout)
	defer cancel()

	return gitIn(ctx, t.TempDir(), dir, args...)
}

// gitIn runs git in dir with home as its HOME and without the config of
// the machine.
func gitIn(ctx context.Context, home, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // the arguments are the test's own
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)

	out, err := cmd.CombinedOutput()

	return string(out), err
}

func gitOK(t *testing.T, dir string, args ...string) string {
	t.Helper()

	out, err := git(t, dir, args...)
	require.NoError(t, err, out)

	return out
}

// clone clones example.com/owner/repo through the broker and returns the
// folder.
func clone(t *testing.T, brokerURL string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "clone")
	gitOK(t, "", "clone", brokerURL+"/example.com/owner/repo", dir)

	return dir
}

func TestBrokerClonesWithTheLoginOfTheHost(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	brokerURL, _ := s.broker(t, Remote{Name: "example.com/owner/repo", Fetch: true, Login: login})

	// act
	dir := filepath.Join(t.TempDir(), "clone")
	out, err := git(t, "", "-c", "http.extraHeader=Authorization: Basic dm06dm0=", "-c", "http.extraHeader=Cookie: a=b", "clone", brokerURL+"/example.com/owner/repo.git", dir)

	// assert
	require.NoError(t, err, out)
	assert.Contains(t, gitOK(t, dir, "log", "--format=%s"), "first")

	s.mu.Lock()
	defer s.mu.Unlock()

	require.NotEmpty(t, s.headers)

	for _, header := range s.headers {
		assert.Equal(t, []string{"Basic aG9zdC11c2VyOmhvc3QtdG9rZW4="}, header.Values("Authorization"))
		assert.Empty(t, header.Values("Cookie"))
	}
}

func TestBrokerRefusesARemoteTheConfigDoesNotName(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	s.repository(t, "owner/other")
	brokerURL, log := s.broker(t, Remote{Name: "example.com/owner/repo", Fetch: true, Login: login})

	// act
	out, err := git(t, "", "clone", brokerURL+"/example.com/owner/other", filepath.Join(t.TempDir(), "clone"))

	// assert
	require.Error(t, err)
	assert.Contains(t, out, "aibox: refused example.com/owner/other: not in git of the project config")
	assert.Contains(t, log.String(), "git refused example.com/owner/other")
}

func TestBrokerRefusesAFetchTheConfigDoesNotAllow(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	brokerURL, _ := s.broker(t, Remote{Name: "example.com/owner/repo", Push: []string{"aibox/*"}, Login: login})

	// act
	out, err := git(t, "", "clone", brokerURL+"/example.com/owner/repo", filepath.Join(t.TempDir(), "clone"))

	// assert
	require.Error(t, err)
	assert.Contains(t, out, "the config does not allow fetch")
}

func TestBrokerPushesToABranchTheConfigAllows(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	brokerURL, log := s.broker(t, Remote{Name: "example.com/owner/repo", Fetch: true, Push: []string{"aibox/*"}, Login: login})
	dir := clone(t, brokerURL)
	gitOK(t, dir, "commit", "--allow-empty", "-m", "second")

	// act
	out, err := git(t, dir, "push", "origin", "HEAD:refs/heads/aibox/work")

	// assert
	require.NoError(t, err, out)

	branch, err := exec.Command("git", "-C", filepath.Join(s.root, "owner/repo.git"), "log", "--format=%s", "aibox/work").Output() //nolint:gosec // the test's own folder
	require.NoError(t, err)
	assert.Contains(t, string(branch), "second")
	assert.Contains(t, log.String(), "git push to example.com/owner/repo: refs/heads/aibox/work")
}

func TestBrokerRefusesAPushTheConfigDoesNotAllow(t *testing.T) {
	cases := map[string]struct {
		args   []string
		reason string
	}{
		"another branch": {[]string{"push", "origin", "HEAD:main"}, "the config allows pushes only to aibox/*"},
		"a delete":       {[]string{"push", "origin", "--delete", "aibox/old"}, "deleting a branch is not allowed"},
		"a tag":          {[]string{"push", "origin", "HEAD:refs/tags/v1"}, "only branches may be pushed"},
		"a nested name":  {[]string{"push", "origin", "HEAD:aibox/a/b"}, "the config allows pushes only to aibox/*"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			// arrange
			s := newServer(t)
			s.repository(t, "owner/repo")
			bare := filepath.Join(s.root, "owner/repo.git")
			gitOK(t, bare, "branch", "aibox/old", "main")
			brokerURL, log := s.broker(t, Remote{Name: "example.com/owner/repo", Fetch: true, Push: []string{"aibox/*"}, Login: login})
			dir := clone(t, brokerURL)
			gitOK(t, dir, "commit", "--allow-empty", "-m", "second")
			before := gitOK(t, bare, "for-each-ref")

			// act
			out, err := git(t, dir, c.args...)

			// assert
			require.Error(t, err)
			assert.Contains(t, out, "remote rejected")
			assert.Contains(t, out, "aibox: "+c.reason)
			assert.Contains(t, log.String(), "git refused push to example.com/owner/repo")
			assert.Equal(t, before, gitOK(t, bare, "for-each-ref"))
		})
	}
}

func TestBrokerRefusesTheWholePushWhenOneRefIsRefused(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	bare := filepath.Join(s.root, "owner/repo.git")
	brokerURL, _ := s.broker(t, Remote{Name: "example.com/owner/repo", Fetch: true, Push: []string{"aibox/*"}, Login: login})
	dir := clone(t, brokerURL)
	gitOK(t, dir, "commit", "--allow-empty", "-m", "second")
	before := gitOK(t, bare, "for-each-ref")

	// act
	out, err := git(t, dir, "push", "origin", "HEAD:aibox/work", "HEAD:main")

	// assert
	require.Error(t, err)
	assert.Contains(t, out, "aibox refused the whole push")
	assert.Equal(t, before, gitOK(t, bare, "for-each-ref"))
}

func TestBrokerSaysWhenTheServerRefusesTheLogin(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	brokerURL, _ := s.broker(t, Remote{Name: "example.com/owner/repo", Fetch: true, Login: Login{Username: "host-user", Password: "expired"}})

	// act
	out, err := git(t, "", "clone", brokerURL+"/example.com/owner/repo", filepath.Join(t.TempDir(), "clone"))

	// assert
	require.Error(t, err)
	assert.Contains(t, out, "502")
	assert.NotContains(t, out, "expired")
}

func TestBrokerRefusesWhatIsNoRequestOfGit(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	brokerURL, _ := s.broker(t, Remote{Name: "example.com/owner/repo", Fetch: true, Push: []string{"*"}, Login: login})

	for _, target := range []string{
		"/example.com/owner/repo",
		"/example.com/owner/repo/info/refs",
		"/example.com/owner/repo/info/refs?service=git-upload-archive",
		"/example.com/owner/repo/info/refs?service=git-upload-pack&x=1",
		"/example.com/owner/repo/HEAD",
		"/example.com/owner/repo/objects/info/packs",
		"/example.com/owner/other/../repo/info/refs?service=git-upload-pack",
		"/example.com/owner/%72epo/info/refs?service=git-upload-pack",
		"/example.com/owner//repo/info/refs?service=git-upload-pack",
		"/example.com/owner/repo\\x/info/refs?service=git-upload-pack",
		"/info/refs?service=git-upload-pack",
	} {
		// act
		response, err := http.Get(brokerURL + target) //nolint:noctx // a test against a local server

		// assert
		require.NoError(t, err)
		_ = response.Body.Close()
		assert.Equal(t, http.StatusForbidden, response.StatusCode, target)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	assert.Empty(t, s.headers)
}

func TestBrokerPushesMoreThanTheBufferOfGit(t *testing.T) {
	// arrange
	s := newServer(t)
	s.repository(t, "owner/repo")
	brokerURL, _ := s.broker(t, Remote{Name: "example.com/owner/repo", Fetch: true, Push: []string{"aibox/*"}, Login: login})
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
	assert.Contains(t, gitOK(t, filepath.Join(s.root, "owner/repo.git"), "log", "--format=%s", "aibox/big"), "big")
}

func TestBrokerRefusesAPushWithoutAReportAsAnError(t *testing.T) {
	// arrange
	brokerURL, log := newServer(t).broker(t, Remote{Name: "example.com/owner/repo", Push: []string{"aibox/*"}, Login: login})
	body := pktLine(oldID+" "+newID+" refs/heads/main\x00side-band-64k\n") + flush

	// act
	response, err := http.Post(brokerURL+"/example.com/owner/repo/git-receive-pack", "application/x-git-receive-pack-request", strings.NewReader(body)) //nolint:noctx // a test against a local server

	// assert
	require.NoError(t, err)
	_ = response.Body.Close()
	assert.Equal(t, http.StatusForbidden, response.StatusCode)
	assert.Contains(t, log.String(), "refused push to example.com/owner/repo: refs/heads/main")
}

func TestEventLogKeepsEachEventOnOnePlainLine(t *testing.T) {
	// arrange
	var out bytes.Buffer

	l := &eventLog{w: &out}

	// act
	l.add("refused push to %s", "a\nb\x1b[31m\u009b31m\u202ec")

	// assert
	assert.Equal(t, 1, strings.Count(out.String(), "\n"))
	assert.Contains(t, out.String(), "refused push to a?b?[31m?31m?c")
}
