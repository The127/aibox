package session

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

const fallbackTerm = "xterm-256color"

var defaultSize = Size{Rows: 24, Cols: 80}

// Client is the terminal on the host that attaches to a session. Resized
// delivers the new size whenever the terminal changes size, and may be nil.
type Client struct {
	In      io.Reader
	Out     io.Writer
	Term    string
	Size    Size
	Resized <-chan Size
	// Env are variables for the command, as NAME=value.
	Env []string
}

// NewClient prepares the terminal on stdin for a session: raw mode, its size
// and size changes. When stdin is not a terminal the client gets a default
// size and no size changes, and a nil stdin types nothing. The returned
// function puts the terminal back.
func NewClient(stdin *os.File, out io.Writer) (Client, func(), error) {
	client := Client{Out: out, Term: os.Getenv("TERM"), Size: defaultSize}
	if client.Term == "" {
		client.Term = fallbackTerm
	}

	nothingToRestore := func() {}

	if stdin == nil {
		return client, nothingToRestore, nil
	}

	client.In = stdin

	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		return client, nothingToRestore, nil
	}

	sizeFd := sizeDescriptor(stdin, out)

	size, err := sizeOfTerminal(sizeFd)
	if err != nil {
		return Client{}, nil, err
	}

	state, err := term.MakeRaw(fd)
	if err != nil {
		return Client{}, nil, err
	}

	client.Size = size

	resized := make(chan Size, 1)
	client.Resized = resized

	stop := make(chan struct{})

	go reportSizes(sizeFd, notifyResize(sizeFd, stop), resized, stop)

	restore := func() {
		close(stop)
		_ = term.Restore(fd, state)
	}

	return client, restore, nil
}

// reportSizes sends the size of the terminal after each change notifyResize
// reports. A size nobody picked up yet is replaced by the newer one.
func reportSizes(fd int, changed <-chan struct{}, resized chan Size, stop <-chan struct{}) {
	for {
		select {
		case <-changed:
			size, err := sizeOfTerminal(fd)
			if err != nil {
				continue
			}

			select {
			case <-resized:
			default:
			}

			resized <- size
		case <-stop:
			return
		}
	}
}

// pollSize reports on the returned channel each time size returns a size
// other than before, read every interval until stop is closed. It is for a
// system that does not say when the terminal changes size.
func pollSize(size func() (Size, error), interval time.Duration, stop <-chan struct{}) <-chan struct{} {
	changed := make(chan struct{}, 1)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// a size that could not be read is no size to compare with
		last, err := size()
		known := err == nil

		for {
			select {
			case <-ticker.C:
				now, err := size()
				if err != nil || now == last {
					continue
				}

				if !known {
					last, known = now, true

					continue
				}

				last = now

				select {
				case changed <- struct{}{}:
				default:
				}
			case <-stop:
				return
			}
		}
	}()

	return changed
}

func sizeOfTerminal(fd int) (Size, error) {
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return Size{}, err
	}

	return Size{Rows: uint16(rows), Cols: uint16(cols)}, nil //nolint:gosec // the kernel reports them as 16 bit values
}

// Attach opens a session on the connection, runs it on the client's
// terminal and returns the exit code of the command.
func (c Client) Attach(conn net.Conn) (int, error) {
	client, s, err := open(conn)
	if err != nil {
		return 0, err
	}

	defer func() { _ = client.Close() }()
	defer func() { _ = s.Close() }()

	s.Stdin = c.In
	s.Stdout = c.Out

	if err := s.RequestPty(c.Term, int(c.Size.Rows), int(c.Size.Cols), ssh.TerminalModes{}); err != nil {
		return 0, fmt.Errorf("request a terminal: %w", err)
	}

	if err := start(s, c.Env); err != nil {
		return 0, err
	}

	stop := make(chan struct{})
	defer close(stop)

	go forwardResizes(s, c.Resized, stop)

	return exitCode(s.Wait())
}

// Exec is a session without a terminal. The command reads In until it ends,
// a nil In sends nothing, and what the command prints goes to Out and, for
// standard error, to Errors. In may still be read after Run returned, until
// it ends or a read fails, so it must not be used for anything else.
type Exec struct {
	In     io.Reader
	Out    io.Writer
	Errors io.Writer
	// Env are variables for the command, as NAME=value.
	Env []string
}

// Run opens a session on the connection, runs the command on pipes and
// returns its exit code once its output has arrived. A command may end
// without reading all of In, which is no error.
func (e Exec) Run(conn net.Conn) (int, error) {
	client, s, err := open(conn)
	if err != nil {
		return 0, err
	}

	defer func() { _ = client.Close() }()
	defer func() { _ = s.Close() }()

	s.Stdout = e.Out
	s.Stderr = e.Errors

	// the session would report a write to a command that is gone as the
	// error of the whole run, so the input is copied here instead
	if e.In != nil {
		stdin, err := s.StdinPipe()
		if err != nil {
			return 0, fmt.Errorf("open the input: %w", err)
		}

		go func() {
			_, _ = io.Copy(stdin, e.In)
			_ = stdin.Close()
		}()
	}

	if err := start(s, e.Env); err != nil {
		return 0, err
	}

	return exitCode(s.Wait())
}

// open does the SSH handshake on the connection and opens a session.
func open(conn net.Conn) (*ssh.Client, *ssh.Session, error) {
	config := &ssh.ClientConfig{
		User: "user",
		// the connection is a vsock to the VM aibox started, which no other
		// party can be on, so the host key adds nothing
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // see above
	}

	clientConn, channels, requests, err := ssh.NewClientConn(conn, "aibox", config)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh handshake: %w", err)
	}

	client := ssh.NewClient(clientConn, channels, requests)

	s, err := client.NewSession()
	if err != nil {
		_ = client.Close()

		return nil, nil, fmt.Errorf("open the session: %w", err)
	}

	return client, s, nil
}

// start sends the variables and starts the command of the session.
func start(s *ssh.Session, env []string) error {
	for _, variable := range env {
		name, value, _ := strings.Cut(variable, "=")
		if err := s.Setenv(name, value); err != nil {
			return fmt.Errorf("send %s: %w", name, err)
		}
	}

	if err := s.Shell(); err != nil {
		return fmt.Errorf("start the command: %w", err)
	}

	return nil
}

func forwardResizes(s *ssh.Session, resized <-chan Size, stop <-chan struct{}) {
	for {
		select {
		case size, ok := <-resized:
			if !ok {
				return
			}

			_ = s.WindowChange(int(size.Rows), int(size.Cols))
		case <-stop:
			return
		}
	}
}

func exitCode(err error) (int, error) {
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus(), nil
	}

	if err != nil {
		return 0, fmt.Errorf("the session ended: %w", err)
	}

	return 0, nil
}
