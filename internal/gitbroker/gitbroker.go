// Package gitbroker lets git in the VM fetch from and push to the remotes
// the project config names, with the git login of the host, so that the VM
// never holds the login. It speaks the smart HTTP protocol of git and
// checks each request against the rules of its remote: fetch or not, and
// the branches a push may update.
package gitbroker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Remote is a repository the VM may use, named as host/path without a
// scheme and without .git, such as github.com/owner/repo.
type Remote struct {
	Name string
	// Fetch allows clone and fetch.
	Fetch bool
	// Push are the branches a push may create or update, as patterns of
	// path.Match, such as aibox/*. Empty refuses every push.
	Push []string
	// Login is what the broker sends upstream for this remote.
	Login Login
}

// Login is a user name and a password or token for HTTP basic auth.
type Login struct {
	Username string
	Password string
}

// Broker returns the handler that serves git in the VM for the remotes. It
// checks the servers with roots, since aibox can read no files once it is
// confined, and writes what it refused and the pushes it passed on to log.
func Broker(remotes []Remote, roots *x509.CertPool, w io.Writer) http.Handler {
	upstream := func(name string) *url.URL {
		host, repository, _ := strings.Cut(name, "/")

		return &url.URL{Scheme: "https", Host: host, Path: "/" + repository + ".git"}
	}

	return newBroker(remotes, upstream, &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}, w)
}

// The services of the smart HTTP protocol.
const (
	uploadPack  = "git-upload-pack"
	receivePack = "git-receive-pack"
)

// forwarded are the headers of the VM the broker passes on. Everything
// else, credentials and cookies too, is dropped.
var forwarded = []string{"Accept", "Content-Type", "Git-Protocol", "User-Agent"}

type broker struct {
	remotes  map[string]Remote
	upstream func(name string) *url.URL
	forward  *httputil.ReverseProxy
	log      *eventLog
}

func newBroker(remotes []Remote, upstream func(name string) *url.URL, transport http.RoundTripper, w io.Writer) http.Handler {
	b := &broker{remotes: map[string]Remote{}, upstream: upstream, log: &eventLog{w: w}}
	for _, remote := range remotes {
		b.remotes[remote.Name] = remote
	}

	b.forward = &httputil.ReverseProxy{
		Rewrite:   b.rewrite,
		Transport: transport,
		// git shows the progress of the server as it comes
		FlushInterval: -1,
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Set-Cookie")

			switch {
			case response.StatusCode == http.StatusUnauthorized:
				return errors.New("the server refused the git login of the host (HTTP 401)")
			case response.StatusCode == http.StatusForbidden:
				return errors.New("the server refused the request, check the git login of the host and its access to the repository (HTTP 403)")
			case response.StatusCode >= 300 && response.StatusCode < 400:
				return fmt.Errorf("the server redirects to %s, name that address in the config instead (HTTP %d)", response.Header.Get("Location"), response.StatusCode)
			}

			return nil
		},
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			req, _ := r.Context().Value(requestKey{}).(request)
			b.log.add("failed %s: %v", req.remote, err)
			http.Error(w, fmt.Sprintf("aibox: %s: %v", req.remote, err), http.StatusBadGateway)
		},
	}

	return b
}

// request is what a request of git asks for.
type request struct {
	remote  string
	service string
	// suffix is the part of the path after the repository
	suffix string
}

// gitPath is a path git sends for a remote: plain letters, digits and
// separators. Escapes, backslashes and other characters are refused, since
// a server could read them as other separators.
var gitPath = regexp.MustCompile(`^/[A-Za-z0-9_.~\-/]+$`)

// parse returns what the request asks for, or false when it is not a
// request of the smart HTTP protocol.
func parse(r *http.Request) (request, bool) {
	p := r.URL.Path
	if r.URL.RawPath != "" || path.Clean(p) != p || !gitPath.MatchString(p) {
		return request{}, false
	}

	var req request

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/info/refs"):
		service, ok := strings.CutPrefix(r.URL.RawQuery, "service=")
		if !ok || service != uploadPack && service != receivePack {
			return request{}, false
		}

		req = request{service: service, suffix: "/info/refs"}
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/"+uploadPack):
		req = request{service: uploadPack, suffix: "/" + uploadPack}
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/"+receivePack):
		req = request{service: receivePack, suffix: "/" + receivePack}
	default:
		return request{}, false
	}

	req.remote = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(p, req.suffix), "/"), ".git")

	return req, req.remote != ""
}

func (b *broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := parse(r)
	if !ok {
		b.refuse(w, "refused %s %q: not a request of git", r.Method, r.URL.Path)

		return
	}

	remote, ok := b.remotes[req.remote]

	switch {
	case !ok:
		b.refuse(w, "refused %s: not in git of the project config", req.remote)

		return
	case req.service == uploadPack && !remote.Fetch:
		b.refuse(w, "refused fetch from %s: the config does not allow fetch", req.remote)

		return
	case req.service == receivePack && len(remote.Push) == 0:
		b.refuse(w, "refused push to %s: the config allows no branches to push to", req.remote)

		return
	}

	if req.service == receivePack && r.Method == http.MethodPost && !b.checkPush(w, r, remote) {
		return
	}

	b.forward.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, req)))
}

// refuse answers with a message that git in the VM shows, and logs it.
func (b *broker) refuse(w http.ResponseWriter, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	b.log.add("%s", message)
	http.Error(w, "aibox: "+message, http.StatusForbidden)
}

func (b *broker) rewrite(r *httputil.ProxyRequest) {
	req, _ := r.In.Context().Value(requestKey{}).(request)

	target := b.upstream(req.remote)
	target.Path += req.suffix

	if req.suffix == "/info/refs" {
		target.RawQuery = "service=" + req.service
	}

	r.Out.URL = target
	r.Out.Host = ""

	header := http.Header{}

	for _, name := range forwarded {
		if values := r.In.Header.Values(name); len(values) > 0 {
			header[name] = values
		}
	}

	// git compresses the requests of a fetch
	if req.service == uploadPack {
		if values := r.In.Header.Values("Content-Encoding"); len(values) > 0 {
			header["Content-Encoding"] = values
		}
	}

	remote := b.remotes[req.remote]
	r.Out.Header = header
	r.Out.SetBasicAuth(remote.Login.Username, remote.Login.Password)
}

// checkPush reads the commands at the start of a push and passes the push
// on only when the config allows every one of them. Otherwise it answers
// for the server and refuses all of them, so that git in the VM shows why.
func (b *broker) checkPush(w http.ResponseWriter, r *http.Request, remote Remote) bool {
	if r.Header.Get("Content-Encoding") != "" {
		b.refuse(w, "refused push to %s: a compressed push", remote.Name)

		return false
	}

	body := bufio.NewReader(r.Body)

	// git sends a push larger than its buffer only after a probe that is
	// a flush alone, and the probe updates nothing
	if head, err := body.Peek(len(flush) + 1); string(head) == flush && errors.Is(err, io.EOF) {
		r.Body = readCloser{bytes.NewReader(head), r.Body}

		return true
	}

	push, err := readPush(body)
	if err != nil {
		b.refuse(w, "refused push to %s: %v", remote.Name, err)

		return false
	}

	refusals := make([]refusal, len(push.commands))
	refused := false

	for i, command := range push.commands {
		refusals[i] = refusal{ref: command.ref, reason: allowedUpdate(command, remote.Push)}
		if refusals[i].reason != "" {
			refused = true

			b.log.add("refused push to %s: %s %s", remote.Name, command.ref, refusals[i].reason)
		}
	}

	if refused {
		if !slices.Contains(push.capabilities, "report-status") && !slices.Contains(push.capabilities, "report-status-v2") {
			b.refuse(w, "refused push to %s: %s %s", remote.Name, refusals[0].ref, refusals[0].reason)

			return false
		}

		// git sends the whole push before it reads the answer
		_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrain))

		for i := range refusals {
			if refusals[i].reason == "" {
				refusals[i].reason = "aibox refused the whole push"
			}
		}

		writeReport(w, push.capabilities, refusals)

		return false
	}

	for _, command := range push.commands {
		b.log.add("push to %s: %s %s..%s", remote.Name, command.ref, command.old, command.new)
	}

	r.Body = readCloser{io.MultiReader(bytes.NewReader(push.raw), body), r.Body}

	return true
}

// maxDrain is how much of a refused push the broker reads before it
// answers, since git reads the answer only once it sent the push. A larger
// push sees the connection close instead of the reasons.
const maxDrain = 64 << 20

// allowedUpdate returns why the config does not allow the command, or
// nothing when it does.
func allowedUpdate(command command, patterns []string) string {
	branch, ok := strings.CutPrefix(command.ref, "refs/heads/")
	if !ok {
		return "only branches may be pushed"
	}

	if command.new == zeroID(len(command.new)) {
		return "deleting a branch is not allowed"
	}

	if !validBranch(branch) {
		return "not a valid branch name"
	}

	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, branch); matched {
			return ""
		}
	}

	return fmt.Sprintf("the config allows pushes only to %s", strings.Join(patterns, ", "))
}

// validBranch tells whether name is a branch name git accepts, as far as
// matching it against a pattern needs: no empty or dot components and none
// of the characters git refuses.
func validBranch(name string) bool {
	if name == "" || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") || strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}

	for component := range strings.SplitSeq(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") {
			return false
		}
	}

	return !strings.ContainsFunc(name, func(r rune) bool {
		return r <= ' ' || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r)
	})
}

func zeroID(n int) string {
	return strings.Repeat("0", n)
}

type readCloser struct {
	io.Reader
	io.Closer
}

// requestKey carries the request to the rewrite and the error handler.
type requestKey struct{}

// eventLog writes what the broker refused and the pushes it passed on, up
// to maxEvents lines, so that a VM that keeps trying cannot fill the log.
type eventLog struct {
	mu    sync.Mutex
	w     io.Writer
	lines int
}

const maxEvents = 1000

func (l *eventLog) add(format string, args ...any) {
	if l.w == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)

	switch {
	case l.lines > maxEvents:
		return
	case l.lines == maxEvents:
		_, _ = fmt.Fprintf(l.w, "%s git %d events, later ones are left out\n", now, maxEvents)
	default:
		_, _ = fmt.Fprintf(l.w, "%s git %s\n", now, sanitize(fmt.Sprintf(format, args...)))
	}

	l.lines++
}

// sanitize keeps a line of the log one plain line, whatever the VM sent:
// no control characters, also not those of eight bits, and none that turn
// the direction of the text.
func sanitize(text string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) && r != ' ' {
			return '?'
		}

		return r
	}, text)
}
