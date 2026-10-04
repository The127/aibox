package session

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

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

	size, err := sizeOfTerminal(fd)
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

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)

	stop := make(chan struct{})

	go reportSizes(fd, winch, resized, stop)

	restore := func() {
		signal.Stop(winch)
		close(stop)
		_ = term.Restore(fd, state)
	}

	return client, restore, nil
}

// reportSizes sends the size of the terminal after each SIGWINCH. A size
// nobody picked up yet is replaced by the newer one.
func reportSizes(fd int, winch <-chan os.Signal, resized chan Size, stop <-chan struct{}) {
	for {
		select {
		case <-winch:
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
	config := &ssh.ClientConfig{
		User: "user",
		// the connection is a vsock to the VM aibox started, which no other
		// party can be on, so the host key adds nothing
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // see above
	}

	clientConn, channels, requests, err := ssh.NewClientConn(conn, "aibox", config)
	if err != nil {
		return 0, fmt.Errorf("ssh handshake: %w", err)
	}

	client := ssh.NewClient(clientConn, channels, requests)
	defer func() { _ = client.Close() }()

	s, err := client.NewSession()
	if err != nil {
		return 0, fmt.Errorf("open the session: %w", err)
	}

	defer func() { _ = s.Close() }()

	s.Stdin = c.In
	s.Stdout = c.Out

	if err := s.RequestPty(c.Term, int(c.Size.Rows), int(c.Size.Cols), ssh.TerminalModes{}); err != nil {
		return 0, fmt.Errorf("request a terminal: %w", err)
	}

	if err := s.Shell(); err != nil {
		return 0, fmt.Errorf("start the command: %w", err)
	}

	stop := make(chan struct{})
	defer close(stop)

	go forwardResizes(s, c.Resized, stop)

	return exitCode(s.Wait())
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
