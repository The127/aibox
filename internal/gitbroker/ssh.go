package gitbroker

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSH is the SSH login of the host for a remote. The broker then speaks
// SSH to the server and HTTP to git in the VM.
type SSH struct {
	// Address is the host and port to dial.
	Address string
	// User is who the broker logs in as, git for most hosts.
	User string
	// Signers are the keys the broker offers.
	Signers []ssh.Signer
	// HostKey checks the key of the server against the known hosts of the
	// host.
	HostKey ssh.HostKeyCallback
	// HostKeyAlgorithms are those of the keys the known hosts hold for the
	// server, which the broker asks the server for.
	HostKeyAlgorithms []string
}

const (
	// dialTimeout is how long the broker waits for a server to connect
	// and log in.
	dialTimeout = 30 * time.Second
	// sessionTimeout is how long the server may take to open a session
	// and to close one.
	sessionTimeout = 10 * time.Second
	// idleTimeout is how long the server may stay silent in its answer. A
	// server sends progress while it packs.
	idleTimeout = 5 * time.Minute
)

// sshServer is the connection of the broker to the server of a remote,
// which it logs in to once and opens a session on for each request.
type sshServer struct {
	// lock guards client. It is a channel, so that a request can stop
	// waiting for it.
	lock   chan struct{}
	client *ssh.Client
}

func newSSHServer() *sshServer {
	return &sshServer{lock: make(chan struct{}, 1)}
}

// serveSSH answers a request of git in the VM by running the service on
// the server over SSH. HTTP is stateless and SSH is not, so each request
// runs the service once: for the advertisement it passes on what the
// server advertises, and for a request it passes on the body after the
// advertisement and returns what the server answers.
func (b *broker) serveSSH(w http.ResponseWriter, r *http.Request, req request, remote Remote) {
	session, done, err := b.session(r.Context(), remote)
	if err != nil {
		b.fail(w, req, err)

		return
	}

	defer done()

	stderr := &lockedBuffer{}
	session.Stderr = stderr

	if protocol := r.Header.Get("Git-Protocol"); gitProtocol.MatchString(protocol) {
		// a server that does not take the variable answers in version 0,
		// which git in the VM understands too
		_ = session.Setenv("GIT_PROTOCOL", protocol)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		b.fail(w, req, err)

		return
	}

	out, err := session.StdoutPipe()
	if err != nil {
		b.fail(w, req, err)

		return
	}

	_, repository, _ := strings.Cut(remote.Name, "/")
	if err := session.Start(req.service + " '" + repository + ".git'"); err != nil {
		b.fail(w, req, err)

		return
	}

	stdout := bufio.NewReader(out)
	isAdvertisement := req.suffix == "/info/refs"

	// a server that stops in the middle of its advertisement does not
	// hold the request forever
	stalled := time.AfterFunc(dialTimeout, func() { _ = session.Close() })
	advertisement, err := readAdvertisement(stdout, isAdvertisement)
	stalled.Stop()

	if err != nil {
		b.fail(w, req, serverError(err, session, stderr))

		return
	}

	if isAdvertisement {
		// git in the VM sends its request in a new connection
		_, _ = io.WriteString(stdin, flush)
		_ = stdin.Close()

		w.Header().Set("Content-Type", "application/x-"+req.service+"-advertisement")
		w.Header().Set("Cache-Control", "no-cache")

		// over HTTP the advertisement of version 0 and 1 starts with the
		// service
		if !bytes.HasPrefix(advertisement, []byte(pktLine("version 2\n"))) {
			_, _ = io.WriteString(w, pktLine("# service="+req.service+"\n")+flush)
		}

		_, _ = w.Write(advertisement)
		b.logExit(req, session, stderr)

		return
	}

	// a server that stops in the middle of its answer does not hold the
	// request forever
	idle := time.AfterFunc(idleTimeout, func() { _ = session.Close() })
	defer idle.Stop()

	body, err := requestBody(r, req)
	if err != nil {
		_ = stdin.Close()
		b.fail(w, req, err)

		return
	}

	// the server answers while git in the VM still sends
	_ = http.NewResponseController(w).EnableFullDuplex()

	sent := make(chan struct{})

	go func() {
		defer close(sent)

		_, _ = io.Copy(stdin, body)
		_ = stdin.Close()
	}()

	w.Header().Set("Content-Type", "application/x-"+req.service+"-result")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(flushWriter{w}, idleReader{stdout, idle})

	b.logExit(req, session, stderr)

	// the body is not to be read once the handler returns
	_ = session.Close()
	<-sent
}

func (b *broker) session(ctx context.Context, remote Remote) (*ssh.Session, func(), error) {
	server := b.sshServers[remote.Name]

	select {
	case server.lock <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}

	session, err := b.openSession(ctx, server, remote)

	<-server.lock

	if err != nil {
		return nil, nil, err
	}

	stop := context.AfterFunc(ctx, func() { _ = session.Close() })

	return session, func() {
		stop()
		_ = session.Close()
	}, nil
}

// openSession opens a session on the connection of the server, and logs in
// again once when there is none or it broke. A connection that does not
// answer within sessionTimeout counts as broken. A server that refuses the
// session, for example since it has too many, keeps the connection, so that
// the sessions on it go on. The caller holds the lock.
func (b *broker) openSession(ctx context.Context, server *sshServer, remote Remote) (*ssh.Session, error) {
	for range 2 {
		if server.client == nil {
			client, err := b.login(ctx, remote)
			if err != nil {
				return nil, err
			}

			server.client = client
		}

		client := server.client
		stalled := time.AfterFunc(sessionTimeout, func() { _ = client.Close() })
		session, err := client.NewSession()
		stalled.Stop()

		if err == nil {
			return session, nil
		}

		var refused *ssh.OpenChannelError
		if errors.As(err, &refused) {
			return nil, fmt.Errorf("the server refused a session: %s", refused.Message)
		}

		_ = client.Close()
		server.client = nil
	}

	return nil, errors.New("the connection to the server broke")
}

// login connects to the server of the remote and logs in, within
// dialTimeout.
func (b *broker) login(ctx context.Context, remote Remote) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:              remote.SSH.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(remote.SSH.Signers...)},
		HostKeyCallback:   remote.SSH.HostKey,
		HostKeyAlgorithms: remote.SSH.HostKeyAlgorithms,
	}

	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, err := b.dial(ctx, "tcp", remote.SSH.Address)
	if err != nil {
		return nil, err
	}

	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)

	c, channels, requests, err := ssh.NewClientConn(conn, remote.SSH.Address, config)
	if err != nil {
		_ = conn.Close()

		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, fmt.Errorf("%s refused the SSH keys of this machine, or the SSH agent did not answer", remote.SSH.Address)
		}

		return nil, err
	}

	_ = conn.SetDeadline(time.Time{})

	return ssh.NewClient(c, channels, requests), nil
}

// requestBody is the body of a request as the service reads it. git
// compresses the requests of a fetch for HTTP, but not over SSH.
func requestBody(r *http.Request, req request) (io.Reader, error) {
	switch encoding := r.Header.Get("Content-Encoding"); {
	case encoding == "":
		return r.Body, nil
	case encoding == "gzip" && req.service == uploadPack:
		return gzip.NewReader(r.Body)
	default:
		return nil, fmt.Errorf("git sent a request compressed with %q, which aibox does not read", encoding)
	}
}

var errNoAdvertisement = errors.New("the server sent no advertisement of git")

// readAdvertisement reads what a service sends first, up to the flush
// that ends it, and returns it when keep is set.
func readAdvertisement(r *bufio.Reader, keep bool) ([]byte, error) {
	var advertisement []byte

	for {
		line, raw, err := readPktLine(r)
		if err != nil {
			return nil, errNoAdvertisement
		}

		if keep {
			advertisement = append(advertisement, raw...)
		}

		if line == nil {
			return advertisement, nil
		}
	}
}

// idleReader reads from r and gives the idle timer of the answer more time
// with each read.
type idleReader struct {
	r    io.Reader
	idle *time.Timer
}

func (i idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	i.idle.Reset(idleTimeout)

	return n, err
}

// wait waits for the service to end, at most sessionTimeout, since a
// server may never confirm that a session closed.
func wait(session *ssh.Session) error {
	result := make(chan error, 1)

	go func() { result <- session.Wait() }()

	select {
	case err := <-result:
		return err
	case <-time.After(sessionTimeout):
		_ = session.Close()

		return errors.New("the server did not end the session")
	}
}

// serverError adds to the error what the server said, which tells, for
// example, that the repository does not exist.
func serverError(err error, session *ssh.Session, stderr *lockedBuffer) error {
	_ = session.Close()
	_ = wait(session)

	if said := strings.TrimSpace(stderr.String()); said != "" {
		return fmt.Errorf("%w: the server said: %s", err, said)
	}

	return err
}

// logExit waits for the service and logs it when it ended with an error
// and said why. A fetch over several requests ends each but the last one
// early, so git in the VM gets what the server sent either way.
func (b *broker) logExit(req request, session *ssh.Session, stderr *lockedBuffer) {
	var exit *ssh.ExitError
	if err := wait(session); errors.As(err, &exit) && strings.TrimSpace(stderr.String()) != "" {
		b.log.add("%s %s ended with %d: %s", req.remote, req.service, exit.ExitStatus(), strings.TrimSpace(stderr.String()))
	}
}

// lockedBuffer is written by the session while the broker reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.String()
}

// flushWriter sends each write to git in the VM at once, so that it shows
// the progress of the server as it comes.
type flushWriter struct {
	w http.ResponseWriter
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	_ = http.NewResponseController(f.w).Flush()

	return n, err
}
