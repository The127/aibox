// Package session carries a terminal session between the host and the VM
// over SSH. The guest serves the session, the host attaches to it.
package session

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrNoSession is returned when the client closes the connection without
// opening a session.
var ErrNoSession = errors.New("the client opened no session")

// drainDelay is how long the server waits for the last output of a process
// that has exited but whose terminal is still held open by a child.
const drainDelay = time.Second

// Size is the size of a terminal in characters.
type Size struct {
	Rows uint16
	Cols uint16
}

// Terminal is the terminal a client asked for.
type Terminal struct {
	Term string
	Size Size
}

// Process is a command running on a terminal. Reads return what it prints
// and writes type into it.
type Process interface {
	io.ReadWriter
	Resize(size Size) error
	Wait() (exitCode int, err error)
}

// Server serves one session on a connection. Start runs the command of the
// session on a terminal.
type Server struct {
	HostKey ssh.Signer
	Start   func(terminal Terminal) (Process, error)
}

// Client is the terminal on the host that attaches to a session. Resized
// delivers the new size whenever the terminal changes size, and may be nil.
type Client struct {
	In      io.Reader
	Out     io.Writer
	Term    string
	Size    Size
	Resized <-chan Size
}

// NewHostKey generates the key a Server identifies itself with.
func NewHostKey() (ssh.Signer, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	return ssh.NewSignerFromKey(key)
}

// Serve runs the SSH server on the connection, serves the first session and
// returns when that session is over.
func (s Server) Serve(conn net.Conn) error {
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(s.HostKey)

	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return fmt.Errorf("ssh handshake: %w", err)
	}

	defer func() { _ = server.Close() }()

	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only a session is served")

			continue
		}

		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return fmt.Errorf("accept the session: %w", err)
		}

		return s.serveSession(channel, channelRequests)
	}

	return ErrNoSession
}

type ptyRequest struct {
	Term     string
	Cols     uint32
	Rows     uint32
	WidthPx  uint32
	HeightPx uint32
	Modes    string
}

type windowChange struct {
	Cols     uint32
	Rows     uint32
	WidthPx  uint32
	HeightPx uint32
}

type exitStatus struct {
	Status uint32
}

type exit struct {
	code int
	err  error
}

func (s Server) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) error {
	defer func() { _ = channel.Close() }()

	var (
		terminal Terminal
		process  Process
	)

	exited := make(chan exit, 1)

	for {
		select {
		case request, ok := <-requests:
			if !ok {
				return nil
			}

			switch request.Type {
			case "pty-req":
				var r ptyRequest

				err := ssh.Unmarshal(request.Payload, &r)
				if err == nil {
					terminal = Terminal{Term: r.Term, Size: sizeOf(r.Rows, r.Cols)}
				}

				reply(request, err == nil)
			case "window-change":
				var r windowChange

				err := ssh.Unmarshal(request.Payload, &r)
				if err == nil && process != nil {
					err = process.Resize(sizeOf(r.Rows, r.Cols))
				}

				reply(request, err == nil && process != nil)
			case "shell":
				if process != nil {
					reply(request, false)

					continue
				}

				started, err := s.Start(terminal)
				if err != nil {
					reply(request, false)

					return fmt.Errorf("start the command: %w", err)
				}

				process = started

				reply(request, true)
				go run(channel, process, exited)
			default:
				reply(request, false)
			}
		case e := <-exited:
			if e.err != nil {
				return fmt.Errorf("wait for the command: %w", e.err)
			}

			_, err := channel.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: uint32(e.code)})) //nolint:gosec // exit codes are small and never negative

			return err
		}
	}
}

// run joins the channel with the process and reports the exit of the
// process once its output has been delivered.
func run(channel ssh.Channel, process Process, exited chan<- exit) {
	go func() { _, _ = io.Copy(process, channel) }()

	outputDone := make(chan struct{})

	go func() {
		_, _ = io.Copy(channel, process)
		close(outputDone)
	}()

	code, err := process.Wait()

	select {
	case <-outputDone:
	case <-time.After(drainDelay):
	}

	exited <- exit{code: code, err: err}
}

func reply(request *ssh.Request, ok bool) {
	if request.WantReply {
		_ = request.Reply(ok, nil)
	}
}

func sizeOf(rows, cols uint32) Size {
	return Size{Rows: uint16(min(rows, math.MaxUint16)), Cols: uint16(min(cols, math.MaxUint16))}
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
	s.Stderr = c.Out

	if err := s.RequestPty(c.Term, int(c.Size.Rows), int(c.Size.Cols), ssh.TerminalModes{}); err != nil {
		return 0, fmt.Errorf("request a terminal: %w", err)
	}

	if err := s.Shell(); err != nil {
		return 0, fmt.Errorf("start the command: %w", err)
	}

	stop := make(chan struct{})
	defer close(stop)

	go func() {
		for {
			select {
			case size, ok := <-c.Resized:
				if !ok {
					return
				}

				_ = s.WindowChange(int(size.Rows), int(size.Cols))
			case <-stop:
				return
			}
		}
	}()

	err = s.Wait()

	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus(), nil
	}

	if err != nil {
		return 0, fmt.Errorf("the session ended: %w", err)
	}

	return 0, nil
}
