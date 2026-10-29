// Package session carries a session between the host and the VM over SSH,
// on a terminal or without one. The guest serves the session, the host
// attaches to it.
package session

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	errNoSession      = errors.New("the client opened no session")
	errAlreadyStarted = errors.New("the session already runs a command")
)

// drainDelay is how long the terminal may stay silent after the command
// has exited before the server stops waiting for a child that still holds
// it. The tests shorten it.
var drainDelay = time.Second

// Size is the size of a terminal in characters.
type Size struct {
	Rows uint16
	Cols uint16
}

// Request is what a client asked for: whether it wants a terminal, the
// TERM and the size of that terminal, and the variables it sent, as
// NAME=value.
type Request struct {
	Terminal bool
	Term     string
	Size     Size
	Env      []string
}

// Process is a command running on a terminal or on pipes. Reads return what
// it prints and writes type into it. Close releases the terminal or the
// pipes after Wait.
type Process interface {
	io.ReadWriteCloser
	Resize(size Size) error
	Wait() (exitCode int, err error)
}

// Piped is a Process without a terminal. Stderr is what it prints to
// standard error, which the session keeps apart from the rest. CloseInput
// ends what it reads once the client has sent everything.
type Piped interface {
	Process
	Stderr() io.Reader
	CloseInput() error
}

// Starter runs the command of a session as the client asked for it.
type Starter func(request Request) (Process, error)

// Serve runs an SSH server on the connection and serves one session for
// each starter, one after another, with a command from that starter. It
// returns when the last session is over. The client may open each session
// once the one before it ended. A session beyond the last starter is
// refused.
func Serve(conn net.Conn, starts ...Starter) error {
	key, err := hostKey()
	if err != nil {
		return err
	}

	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(key)

	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return fmt.Errorf("ssh handshake: %w", err)
	}

	defer func() { _ = server.Close() }()

	go ssh.DiscardRequests(requests)

	sessions := make(chan ssh.NewChannel, len(starts))
	go dispatch(channels, sessions)

	for _, start := range starts {
		newChannel, ok := <-sessions
		if !ok {
			return errNoSession
		}

		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return fmt.Errorf("accept the session: %w", err)
		}

		s := &served{channel: channel, start: start, exited: make(chan result, 1)}

		if err := s.serve(channelRequests); err != nil {
			return err
		}
	}

	return nil
}

// dispatch passes as many sessions to sessions as it has room for, refuses
// the sessions after them and serves the other channels on their own.
func dispatch(channels <-chan ssh.NewChannel, sessions chan<- ssh.NewChannel) {
	defer close(sessions)

	left := cap(sessions)

	for newChannel := range channels {
		switch {
		case newChannel.ChannelType() != "session":
			go openOther(newChannel)
		case left > 0:
			left--
			sessions <- newChannel
		default:
			_ = newChannel.Reject(ssh.Prohibited, "no more sessions are served")
		}
	}
}

// vmLoopback is the address a client may connect to through the session:
// the loopback of the VM, where its servers listen.
const vmLoopback = "127.0.0.1"

// directTCPIP is what a client sends to connect to a port, as RFC 4254
// says in section 7.2.
type directTCPIP struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

// openOther serves a channel besides the session: a connection to a port
// on the loopback of the VM. It turns away any other channel.
func openOther(newChannel ssh.NewChannel) {
	if newChannel.ChannelType() != "direct-tcpip" {
		_ = newChannel.Reject(ssh.UnknownChannelType, "only a session and connections to the loopback are served")

		return
	}

	var target directTCPIP
	if err := ssh.Unmarshal(newChannel.ExtraData(), &target); err != nil || target.Host != vmLoopback || target.Port == 0 || target.Port > math.MaxUint16 {
		_ = newChannel.Reject(ssh.Prohibited, "only a port on "+vmLoopback+" is served")

		return
	}

	conn, err := net.Dial("tcp", net.JoinHostPort(vmLoopback, strconv.Itoa(int(target.Port))))
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())

		return
	}

	channel, requests, err := newChannel.Accept()
	if err != nil {
		_ = conn.Close()

		return
	}

	go ssh.DiscardRequests(requests)
	go join(channel, conn.(*net.TCPConn))
}

// join copies bytes between the channel and the connection until both
// sides have stopped sending, and passes the end of each side on as a half
// close.
func join(channel ssh.Channel, conn *net.TCPConn) {
	defer func() { _ = channel.Close() }()
	defer func() { _ = conn.Close() }()

	var directions sync.WaitGroup

	directions.Go(func() {
		_, _ = io.Copy(channel, conn)
		_ = channel.CloseWrite()
	})

	directions.Go(func() {
		_, _ = io.Copy(conn, channel)
		_ = conn.CloseWrite()
	})

	directions.Wait()
}

// hostKey is new for every session. The host does not check it, because the
// connection to the VM already tells it who it talks to.
func hostKey() (ssh.Signer, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate the host key: %w", err)
	}

	return ssh.NewSignerFromKey(key)
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

type envRequest struct {
	Name  string
	Value string
}

type exitStatus struct {
	Status uint32
}

type result struct {
	code int
	err  error
}

// served is one session being served.
type served struct {
	channel ssh.Channel
	start   Starter
	request Request
	process Process
	exited  chan result
}

func (s *served) serve(requests <-chan *ssh.Request) error {
	defer func() { _ = s.channel.Close() }()

	defer func() {
		if s.process != nil {
			_ = s.process.Close()
		}
	}()

	for {
		select {
		case request, ok := <-requests:
			if !ok {
				return nil
			}

			if err := s.handle(request); err != nil {
				return err
			}
		case r := <-s.exited:
			if r.err != nil {
				return fmt.Errorf("wait for the command: %w", r.err)
			}

			_, err := s.channel.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: uint32(r.code)})) //nolint:gosec // exit codes are small and never negative
			if err != nil {
				return err
			}

			_ = s.channel.Close()
			awaitClose(requests)

			return nil
		}
	}
}

// awaitClose waits for the client to close the channel too, so that the
// connection is not torn down while the client still has the exit status
// to read. A client that does not close is not waited for.
func awaitClose(requests <-chan *ssh.Request) {
	closed := make(chan struct{})

	go func() {
		for request := range requests {
			reply(request, false)
		}

		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(drainDelay):
	}
}

func (s *served) handle(request *ssh.Request) error {
	switch request.Type {
	case "pty-req":
		var r ptyRequest

		err := ssh.Unmarshal(request.Payload, &r)
		if err == nil {
			s.request.Terminal = true
			s.request.Term = r.Term
			s.request.Size = sizeOf(r.Rows, r.Cols)
		}

		reply(request, err == nil)
	case "window-change":
		var r windowChange

		err := ssh.Unmarshal(request.Payload, &r)
		if err == nil {
			err = s.resize(sizeOf(r.Rows, r.Cols))
		}

		reply(request, err == nil)
	case "env":
		var r envRequest

		err := ssh.Unmarshal(request.Payload, &r)
		if err == nil {
			s.request.Env = append(s.request.Env, r.Name+"="+r.Value)
		}

		reply(request, err == nil)
	case "shell":
		err := s.startCommand()
		reply(request, err == nil)

		return err
	default:
		reply(request, false)
	}

	return nil
}

func (s *served) resize(size Size) error {
	if s.process == nil {
		s.request.Size = size

		return nil
	}

	return s.process.Resize(size)
}

func (s *served) startCommand() error {
	if s.process != nil {
		return errAlreadyStarted
	}

	process, err := s.start(s.request)
	if err != nil {
		return fmt.Errorf("start the command: %w", err)
	}

	s.process = process

	go relay(s.channel, process, s.exited)

	return nil
}

// relay joins the channel with the process and reports the exit of the
// process once its output has been delivered. After the exit, the terminal
// or the pipes end when their last user closes them. A child that keeps
// them open and silent is not waited for, a slow client is. A process on
// pipes sends standard error apart and learns when the client has sent
// everything.
func relay(channel ssh.Channel, process Process, exited chan<- result) {
	piped, isPiped := process.(Piped)

	go func() {
		_, _ = io.Copy(process, channel)

		if isPiped {
			_ = piped.CloseInput()
		}
	}()

	outputs := []*outputCopy{{from: process, to: channel, done: make(chan struct{})}}
	if isPiped {
		outputs = append(outputs, &outputCopy{from: piped.Stderr(), to: channel.Stderr(), done: make(chan struct{})})
	}

	for _, output := range outputs {
		go output.run()
	}

	code, err := process.Wait()

	var drained sync.WaitGroup

	for _, output := range outputs {
		drained.Go(func() { output.await(drainDelay) })
	}

	drained.Wait()

	exited <- result{code: code, err: err}
}

// outputCopy copies the output of the process to the channel and records
// since when it has been blocked reading from the process.
type outputCopy struct {
	from         io.Reader
	to           io.Writer
	done         chan struct{}
	readingSince atomic.Int64
}

func (c *outputCopy) run() {
	defer close(c.done)

	buffer := make([]byte, 32*1024)

	for {
		c.readingSince.Store(time.Now().UnixNano())
		n, err := c.from.Read(buffer)
		c.readingSince.Store(0)

		if n > 0 {
			if _, err := c.to.Write(buffer[:n]); err != nil {
				return
			}
		}

		if err != nil {
			return
		}
	}
}

// await returns when the copy has ended or when it has been waiting for
// output for the delay.
func (c *outputCopy) await(delay time.Duration) {
	ticker := time.NewTicker(delay / 10)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			since := c.readingSince.Load()
			if since != 0 && time.Since(time.Unix(0, since)) >= delay {
				return
			}
		}
	}
}

func reply(request *ssh.Request, ok bool) {
	if request.WantReply {
		_ = request.Reply(ok, nil)
	}
}

func sizeOf(rows, cols uint32) Size {
	return Size{Rows: uint16(min(rows, math.MaxUint16)), Cols: uint16(min(cols, math.MaxUint16))}
}
