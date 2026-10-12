package hvvm

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/appcontainer"
	"github.com/the127/aibox/internal/hvchild"
)

// Events take what the helper says its proxy connected to, refused, and
// closed, as proxy.Options does.
type Events struct {
	OnConnected func(target string)
	OnRefused   func(target string)
	OnClosed    func(target string)
}

// Helper is the helper as aibox talks to it.
type Helper struct {
	conn    *hvchild.Conn
	process *appcontainer.Process
	done    chan struct{}
	answers chan answer

	// copies end once aibox wrote all the helper said on its stderr
	copies []chan struct{}
}

type answer struct {
	kind string
	body json.RawMessage
}

// ErrNoAnswer is a helper that ended before it said whether it serves.
var ErrNoAnswer = errors.New("the helper that serves the proxy ended without an answer")

// Connect is aibox's side of a helper on the pipes r and w, which the
// closers close: it passes the events of its proxy on to events, and reads
// no message longer than the helper's can be, as a VM may have taken the
// helper over.
func Connect(r io.Reader, w io.Writer, events Events, closers ...io.Closer) *Helper {
	h := &Helper{
		conn:    hvchild.New(r, w, maxFromHelper(), closers...),
		done:    make(chan struct{}),
		answers: make(chan answer, 1),
	}

	go func() {
		defer close(h.done)

		_ = h.conn.Serve(func(kind string, body json.RawMessage) { h.message(kind, body, events) })
	}()

	return h
}

func (h *Helper) message(kind string, body json.RawMessage, events Events) {
	switch kind {
	case kindReady, kindFailed:
		select {
		case h.answers <- answer{kind, body}:
		default:
		}
	case kindProxy:
		var e proxyEvent
		if json.Unmarshal(body, &e) != nil || !isTarget(e.Target) {
			return
		}

		var on func(string)

		switch e.Event {
		case proxyConnected:
			on = events.OnConnected
		case proxyRefused:
			on = events.OnRefused
		case proxyClosed:
			on = events.OnClosed
		}

		if on != nil {
			on(e.Target)
		}
	}
}

// Configure gives the helper its config and returns once it serves, or
// with why it can't.
func (h *Helper) Configure(ctx context.Context, c Config) error {
	if err := h.conn.Send(kindConfig, c); err != nil {
		return fmt.Errorf("give the helper its config: %w", err)
	}

	select {
	case a := <-h.answers:
		if a.kind == kindReady {
			return nil
		}

		var text string
		_ = json.Unmarshal(a.body, &text)

		return fmt.Errorf("serve the proxy: %s", hvchild.Printable(text))
	case <-h.done:
		return ErrNoAnswer
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done is closed when the helper ended.
func (h *Helper) Done() <-chan struct{} { return h.done }

// Close ends the helper, which has nothing to finish: it ends it rather
// than wait for it to see the pipes close.
func (h *Helper) Close() error {
	err := h.conn.Close()

	if h.process != nil {
		err = errors.Join(err, h.process.Kill())

		if _, waitErr := h.process.Wait(); waitErr != nil {
			err = errors.Join(err, waitErr)
		}

		err = errors.Join(err, h.process.Close())
	}

	<-h.done

	for _, copied := range h.copies {
		<-copied
	}

	return err
}

// ServeServices answers the requests the helper hands to aibox on l, each
// with the handler local returns for the port of the loopback of the VM
// it names, and only those with the token: other processes of the host
// can reach l too. It serves until the returned function is called.
func ServeServices(l net.Listener, token string, local func(host, port string) http.Handler) func() {
	// a handler that takes the connection over, as the IDE bridge does,
	// goes on after the server closed, so stopping waits for every handler,
	// which the end of their context asks to end; once it stops, no
	// handler may start, or it could start after the wait
	ctx, cancel := context.WithCancel(context.Background())

	var (
		handlers sync.WaitGroup
		mu       sync.Mutex
		done     bool
	)

	server := &http.Server{ //nolint:gosec // the helper sends headers it has read in full, and one that stalls holds only its own connection, which ends with it
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(token)) != 1 {
				http.Error(w, "no request of the helper of aibox", http.StatusForbidden)

				return
			}

			port := r.Header.Get(portHeader)
			r.Header.Del(tokenHeader)
			r.Header.Del(portHeader)

			handler := local("localhost", port)
			if handler == nil {
				http.NotFound(w, r)

				return
			}

			mu.Lock()
			if done {
				mu.Unlock()
				http.Error(w, "aibox is ending", http.StatusServiceUnavailable)

				return
			}

			handlers.Add(1)
			mu.Unlock()

			defer handlers.Done()

			handler.ServeHTTP(w, r)
		}),
		BaseContext: func(net.Listener) context.Context { return ctx },
		ErrorLog:    log.New(io.Discard, "", 0),
	}

	served := make(chan struct{})

	go func() {
		defer close(served)

		_ = server.Serve(l)
	}()

	return func() {
		_ = server.Close()

		mu.Lock()
		done = true
		mu.Unlock()

		cancel()
		handlers.Wait()
		<-served
	}
}

// Spec is how aibox starts the helper: itself, the program at Self, in
// the job, in the container unless it is nil, with what fails in its
// proxy said on Stderr, and what takes the events of the proxy.
type Spec struct {
	Self      string
	Job       *appcontainer.Job
	Container *windows.SID
	Stderr    io.Writer
	Events    Events
}

// Start starts the helper and returns once it serves as c says.
func Start(ctx context.Context, s Spec, c Config) (*Helper, error) {
	p := pipes{}

	h, err := p.start(ctx, s, c)
	if err != nil {
		p.close()

		return nil, err
	}

	return h, nil
}

// pipes are the pipes aibox makes for a helper it starts.
type pipes struct {
	// its are the helper's ends, which aibox closes once it started it
	its []*os.File
	// ours are aibox's ends, which the helper's Conn closes once there is one
	ours []*os.File
	// copies end once aibox wrote all the helper said on its stderr
	copies []chan struct{}
}

// stderr is a pipe for the helper to say what fails, which aibox copies to
// w as it would what the VM says: never a handle of aibox's own, which
// could be the console.
func (p *pipes) stderr(w io.Writer) (*os.File, error) {
	r, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	p.its = append(p.its, pw)
	copied := make(chan struct{})
	p.copies = append(p.copies, copied)

	go func() {
		defer close(copied)

		copyPrintable(w, r)
		_ = r.Close()
	}()

	return pw, nil
}

// copyPrintable copies r to w without the control characters but the ends
// of lines, which could move the cursor, forge a line or talk to the
// terminal, as it comes.
func copyPrintable(w io.Writer, r io.Reader) {
	in := bufio.NewReader(r)
	out := make([]byte, 0, in.Size())

	for {
		c, size, err := in.ReadRune()
		if err != nil {
			_, _ = w.Write(out)

			return
		}

		if (c == utf8.RuneError && size == 1) || (c != '\n' && unicode.IsControl(c)) {
			c = '?'
		}

		out = utf8.AppendRune(out, c)

		// what came is written once it is read, so no line waits for its end
		if c == '\n' || in.Buffered() == 0 {
			if _, err := w.Write(out); err != nil {
				_, _ = io.Copy(io.Discard, in)

				return
			}

			out = out[:0]
		}
	}
}

// closeIts closes the helper's ends: once it has them, aibox must not hold
// them, or neither the helper nor the copies would see their input end.
func (p *pipes) closeIts() {
	for _, f := range p.its {
		_ = f.Close()
	}

	p.its = nil
}

func (p *pipes) close() {
	p.closeIts()

	for _, f := range p.ours {
		_ = f.Close()
	}

	for _, copied := range p.copies {
		<-copied
	}
}

func (p *pipes) start(ctx context.Context, s Spec, c Config) (*Helper, error) {
	itsIn, toIt, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	p.its, p.ours = append(p.its, itsIn), append(p.ours, toIt)

	fromIt, itsOut, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	p.its, p.ours = append(p.its, itsOut), append(p.ours, fromIt)

	spec := appcontainer.Spec{
		Path:    s.Self,
		Args:    []string{s.Self, Command, strconv.FormatUint(uint64(itsIn.Fd()), 10), strconv.FormatUint(uint64(itsOut.Fd()), 10)},
		Env:     appcontainer.SystemEnv(),
		Inherit: []windows.Handle{windows.Handle(itsIn.Fd()), windows.Handle(itsOut.Fd())},
		Job:     s.Job,
	}

	if s.Stderr != nil {
		stderr, err := p.stderr(s.Stderr)
		if err != nil {
			return nil, err
		}

		spec.Stderr = windows.Handle(stderr.Fd())
	}

	if s.Container != nil {
		internet, err := windows.StringToSid(appcontainer.InternetClient)
		if err != nil {
			return nil, err
		}

		spec.Container, spec.Capabilities = s.Container, []*windows.SID{internet}
	}

	process, err := appcontainer.Start(spec)
	if err != nil {
		return nil, err
	}

	p.closeIts()

	h := Connect(fromIt, toIt, s.Events, fromIt, toIt)
	h.process, h.copies = process, p.copies
	p.ours, p.copies = nil, nil

	if err := h.Configure(ctx, c); err != nil {
		_ = h.Close()

		return nil, err
	}

	return h, nil
}
