package session_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/session"
)

// fakeProcess stands in for the command behind the terminal. The test
// writes what the process prints into output and reads what the client
// typed from typed.
type fakeProcess struct {
	terminal session.Terminal
	output   *io.PipeReader
	typed    *syncBuffer
	resized  chan session.Size
	exit     chan int
	started  chan struct{}
	waited   chan struct{}
	closed   chan struct{}
}

func (p *fakeProcess) Read(b []byte) (int, error)  { return p.output.Read(b) }
func (p *fakeProcess) Write(b []byte) (int, error) { return p.typed.Write(b) }

func (p *fakeProcess) Resize(size session.Size) error {
	p.resized <- size

	return nil
}

func (p *fakeProcess) Wait() (int, error) {
	code := <-p.exit
	close(p.waited)

	return code, nil
}

func (p *fakeProcess) Close() error {
	close(p.closed)

	return nil
}

type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

// fixture joins a server and a client over an in-memory connection.
type fixture struct {
	process  *fakeProcess
	prints   *io.PipeWriter
	screen   *syncBuffer
	keyboard *io.PipeWriter
	resizes  chan session.Size
	served   chan error
	attached chan attachResult
}

type attachResult struct {
	code int
	err  error
}

func newFixture(t *testing.T, startErr error, clientOptions ...func(*session.Client)) *fixture {
	t.Helper()

	output, prints := io.Pipe()
	typedByClient, keyboard := io.Pipe()

	f := &fixture{
		process: &fakeProcess{
			output:  output,
			typed:   &syncBuffer{},
			resized: make(chan session.Size, 10),
			exit:    make(chan int, 1),
			started: make(chan struct{}),
			waited:  make(chan struct{}),
			closed:  make(chan struct{}),
		},
		prints:   prints,
		screen:   &syncBuffer{},
		keyboard: keyboard,
		resizes:  make(chan session.Size),
		served:   make(chan error, 1),
		attached: make(chan attachResult, 1),
	}

	start := func(terminal session.Terminal) (session.Process, error) {
		if startErr != nil {
			return nil, startErr
		}

		f.process.terminal = terminal
		close(f.process.started)

		return f.process, nil
	}

	client := session.Client{
		In:      typedByClient,
		Out:     f.screen,
		Term:    "xterm-kitty",
		Size:    session.Size{Rows: 50, Cols: 160},
		Resized: f.resizes,
	}

	for _, option := range clientOptions {
		option(&client)
	}

	guestSide, hostSide := connPair(t)

	go func() { f.served <- session.Serve(guestSide, start) }()

	go func() {
		code, err := client.Attach(hostSide)
		f.attached <- attachResult{code, err}
	}()

	return f
}

// connPair is a pair of connected sockets. Unlike net.Pipe they buffer, which
// the SSH handshake needs.
func connPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = listener.Close() }()

	accepted := make(chan net.Conn, 1)

	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	dialed, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)

	conn := <-accepted
	t.Cleanup(func() { _ = conn.Close(); _ = dialed.Close() })

	return conn, dialed
}

const patience = 10 * time.Second

func (f *fixture) waitStarted(t *testing.T) {
	t.Helper()

	select {
	case <-f.process.started:
	case <-time.After(patience):
		t.Fatal("the process was not started")
	}
}

// exited lets the process exit with the code and returns once the server
// has seen the exit.
func (f *fixture) exited(t *testing.T, code int) {
	t.Helper()
	f.process.exit <- code

	select {
	case <-f.process.waited:
	case <-time.After(patience):
		t.Fatal("Wait was not called")
	}
}

// waitAttached is what the client came back with, or the test fails.
func (f *fixture) waitAttached(t *testing.T) attachResult {
	t.Helper()

	select {
	case result := <-f.attached:
		return result
	case <-time.After(patience):
		t.Fatal("Attach did not return")

		return attachResult{}
	}
}

// waitServed is what the server came back with, or the test fails.
func (f *fixture) waitServed(t *testing.T) error {
	t.Helper()

	select {
	case err := <-f.served:
		return err
	case <-time.After(patience):
		t.Fatal("Serve did not return")

		return nil
	}
}

// end lets the process exit with the code and returns what the client and
// the server came back with.
func (f *fixture) end(t *testing.T, code int) (attachResult, error) {
	t.Helper()

	_ = f.prints.Close()
	f.process.exit <- code

	return f.waitAttached(t), f.waitServed(t)
}

func TestSessionShowsTheClientWhatTheProcessPrints(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.waitStarted(t)

	// act
	_, err := io.WriteString(f.prints, "hello from the VM\r\n")

	// assert
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return f.screen.String() == "hello from the VM\r\n" }, 5*time.Second, 10*time.Millisecond)
}

func TestSessionTypesWhatTheClientSendsIntoTheProcess(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.waitStarted(t)

	// act
	_, err := io.WriteString(f.keyboard, "ls\r")

	// assert
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return f.process.typed.String() == "ls\r" }, 5*time.Second, 10*time.Millisecond)
}

func TestSessionStartsTheProcessWithTheClientsTerminal(t *testing.T) {
	// arrange
	f := newFixture(t, nil)

	// act
	f.waitStarted(t)

	// assert
	assert.Equal(t, session.Terminal{Term: "xterm-kitty", Size: session.Size{Rows: 50, Cols: 160}}, f.process.terminal)
}

func TestSessionResizesTheProcessWithTheClient(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.waitStarted(t)

	// act
	f.resizes <- session.Size{Rows: 30, Cols: 100}

	// assert
	select {
	case size := <-f.process.resized:
		assert.Equal(t, session.Size{Rows: 30, Cols: 100}, size)
	case <-time.After(patience):
		t.Fatal("the process was not resized")
	}
}

func TestSessionReturnsTheExitCodeOfTheProcess(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.waitStarted(t)

	// act
	result, served := f.end(t, 7)

	// assert
	require.NoError(t, served)
	require.NoError(t, result.err)
	assert.Equal(t, 7, result.code)
}

func TestSessionEndsWithZeroWhenTheProcessSucceeds(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.waitStarted(t)

	// act
	result, served := f.end(t, 0)

	// assert
	require.NoError(t, served)
	require.NoError(t, result.err)
	assert.Equal(t, 0, result.code)
}

func TestSessionDeliversOutputThatKeepsComingAfterTheExit(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.waitStarted(t)
	f.exited(t, 0)

	// act
	for range 10 {
		time.Sleep(200 * time.Millisecond)
		_, err := io.WriteString(f.prints, "still here\r\n")
		require.NoError(t, err)
	}

	_ = f.prints.Close()

	// assert
	result := f.waitAttached(t)
	require.NoError(t, result.err)
	assert.Equal(t, strings.Repeat("still here\r\n", 10), f.screen.String())
}

// blockable is a screen that holds up every write until released.
type blockable struct {
	syncBuffer
	release chan struct{}
}

func (b *blockable) Write(p []byte) (int, error) {
	<-b.release

	return b.syncBuffer.Write(p)
}

func TestSessionWaitsForAClientThatReadsSlowly(t *testing.T) {
	// arrange
	screen := &blockable{release: make(chan struct{})}
	f := newFixture(t, nil, func(c *session.Client) { c.Out = screen })
	f.waitStarted(t)
	output := strings.Repeat("x", 4<<20)

	go func() {
		_, _ = io.WriteString(f.prints, output)
		_ = f.prints.Close()
	}()

	f.exited(t, 0)

	// act
	time.Sleep(2 * time.Second)
	close(screen.release)

	// assert
	result := f.waitAttached(t)
	require.NoError(t, result.err)
	assert.Len(t, screen.String(), len(output))
}

func TestSessionGivesUpOnATerminalThatStaysSilentAfterTheExit(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.waitStarted(t)
	start := time.Now()

	// act
	f.process.exit <- 0

	// assert
	result := f.waitAttached(t)
	require.NoError(t, result.err)
	assert.Equal(t, 0, result.code)
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestSessionClosesTheProcessWhenItIsOver(t *testing.T) {
	// arrange
	f := newFixture(t, nil)
	f.waitStarted(t)

	// act
	_, _ = f.end(t, 0)

	// assert
	select {
	case <-f.process.closed:
	case <-time.After(patience):
		t.Fatal("the process was not closed")
	}
}

func TestSessionFailsWhenTheProcessCannotStart(t *testing.T) {
	// arrange
	f := newFixture(t, errors.New("no such file"))

	// act
	result := f.waitAttached(t)

	// assert
	require.ErrorContains(t, result.err, "start the command")
	assert.ErrorContains(t, f.waitServed(t), "no such file")
}

func TestAttachFailsWhenTheOtherSideIsNotASession(t *testing.T) {
	// arrange
	guestSide, hostSide := connPair(t)

	go func() {
		_, _ = io.WriteString(guestSide, "HTTP/1.1 400 Bad Request\r\n\r\n")
		_ = guestSide.Close()
	}()

	// act
	_, err := session.Client{In: bytes.NewReader(nil), Out: io.Discard}.Attach(hostSide)

	// assert
	assert.ErrorContains(t, err, "ssh handshake")
}
