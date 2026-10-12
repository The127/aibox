package hvshares

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/appcontainer"
	"github.com/the127/aibox/internal/hvchild"
	"github.com/the127/aibox/internal/hvsock"
	"github.com/the127/aibox/internal/ninefs"
)

// Config is what the helper serves: the shares, with the owner the guest
// sees, on the port of the VM.
type Config struct {
	VM     guid.GUID      `json:"vm"`
	Port   uint32         `json:"port"`
	Shares []ninefs.Share `json:"shares"`
	Owner  ninefs.Owner   `json:"owner"`
}

// The messages between aibox and the helper: aibox sends the config, and
// the helper answers that it serves, or why it can't.
const (
	kindConfig = "config"
	kindReady  = "ready"
	kindFailed = "failed"
)

// Serve is the helper: it takes the config aibox sends on in, serves the
// shares, says so on out, and serves them until in ends, which it does
// when aibox ends.
func Serve(in io.Reader, out io.Writer) error {
	return serve(in, out, hvsock.Listen)
}

func serve(in io.Reader, out io.Writer, listen func(vm guid.GUID, port uint32) (net.Listener, error)) error {
	conn := hvchild.New(in, out, 0)

	configs := make(chan Config, 1)
	served := make(chan error, 1)

	go func() {
		served <- conn.Serve(func(kind string, body json.RawMessage) {
			var c Config
			if kind == kindConfig && json.Unmarshal(body, &c) == nil {
				select {
				case configs <- c:
				default:
				}
			}
		})
	}()

	var c Config

	select {
	case c = <-configs:
	case err := <-served:
		return err
	}

	fs, err := ninefs.New(c.Shares, c.Owner)
	if err != nil {
		// aibox says why, rather than both of them
		return conn.Send(kindFailed, err.Error())
	}

	defer func() { _ = fs.Close() }()

	l, err := listen(c.VM, c.Port)
	if err != nil {
		// aibox says why, rather than both of them
		return conn.Send(kindFailed, err.Error())
	}

	defer func() { _ = l.Close() }()

	go serveShares(l, fs)

	if err := conn.Send(kindReady, nil); err != nil {
		return err
	}

	return <-served
}

// serveShares serves the folders of the host to each connection the guest
// makes to the listener, as the guest mounts each share over a connection
// of its own, until the listener closes.
func serveShares(l net.Listener, fs p9.Attacher) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}

		go func() { _ = p9.NewServer(fs).Handle(conn, conn) }()
	}
}

// Helper is the helper as aibox started it. Close ends it, once.
type Helper struct {
	process *appcontainer.Process
	conn    *hvchild.Conn
	done    chan struct{}
	errors  chan struct{}
}

// Done is closed when the helper ended, which it does before aibox only
// if it failed, after which the VM has its folders no more.
func (h *Helper) Done() <-chan struct{} { return h.done }

// ErrNoAnswer is a helper that ended without saying whether it serves.
var ErrNoAnswer = errors.New("the helper that serves the shares ended without an answer")

// Start starts the helper, the program at self, in the job, with what it
// says of its errors going to stderr, and returns once it serves the
// shares of c. It has the rights of the user, as it serves the user's
// folders, so nothing it sends could do more than it can itself, and
// aibox reads it as it reads itself: with no limit on its messages.
func Start(ctx context.Context, self string, job *appcontainer.Job, stderr io.Writer, c Config) (*Helper, error) {
	// a pipe, as stderr may be no file, and a console window of its own
	// that the helper would write to in the middle of the terminal
	errsFromIt, itsErr, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	copied := make(chan struct{})

	go func() {
		defer close(copied)

		_, _ = io.Copy(stderr, errsFromIt)
		_ = errsFromIt.Close()
	}()

	h, err := start(ctx, self, job, itsErr, c)

	// the helper's end of it, which the copy ends with
	_ = itsErr.Close()

	if err != nil {
		<-copied

		return nil, err
	}

	h.errors = copied

	return h, nil
}

func start(ctx context.Context, self string, job *appcontainer.Job, stderr *os.File, c Config) (*Helper, error) {
	itsIn, toIt, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	fromIt, itsOut, err := os.Pipe()
	if err != nil {
		_ = itsIn.Close()
		_ = toIt.Close()

		return nil, err
	}

	process, err := appcontainer.Start(appcontainer.Spec{
		Path:   self,
		Args:   []string{self, Command},
		Env:    appcontainer.SystemEnv(),
		Stdin:  windows.Handle(itsIn.Fd()),
		Stdout: windows.Handle(itsOut.Fd()),
		Stderr: windows.Handle(stderr.Fd()),
		Job:    job,
	})

	// the helper has its ends now, which aibox must not hold, or the
	// helper would never see aibox end
	_ = itsIn.Close()
	_ = itsOut.Close()

	if err != nil {
		_ = toIt.Close()
		_ = fromIt.Close()

		return nil, err
	}

	h := &Helper{process: process, done: make(chan struct{})}
	h.conn = hvchild.New(fromIt, toIt, 0, fromIt, toIt)

	answers := make(chan answer, 1)

	go func() {
		defer close(h.done)

		_ = h.conn.Serve(func(kind string, body json.RawMessage) {
			select {
			case answers <- answer{kind, body}:
			default:
			}
		})
	}()

	if err := h.conn.Send(kindConfig, c); err != nil {
		_ = h.Close()

		return nil, fmt.Errorf("give the helper its shares: %w", err)
	}

	select {
	case a := <-answers:
		if a.kind == kindReady {
			return h, nil
		}

		var text string
		_ = json.Unmarshal(a.body, &text)
		_ = h.Close()

		return nil, fmt.Errorf("serve the shares: %s", hvchild.Printable(text))
	case <-h.done:
		_ = h.Close()

		return nil, ErrNoAnswer
	case <-ctx.Done():
		_ = h.Close()

		return nil, ctx.Err()
	}
}

type answer struct {
	kind string
	body json.RawMessage
}

// Close ends the helper, which has nothing to finish: it ends it rather
// than wait for it to see its input end.
func (h *Helper) Close() error {
	err := errors.Join(h.conn.Close(), h.process.Kill())

	if _, waitErr := h.process.Wait(); waitErr != nil {
		err = errors.Join(err, waitErr)
	}

	// what it said last, before it ended
	if h.errors != nil {
		<-h.errors
	}

	return errors.Join(err, h.process.Close())
}
