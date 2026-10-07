package session_test

import (
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/session"
)

// fakePiped is a process on pipes. The test writes what it prints to
// standard error into errors. With refuseInput it takes no input, like a
// command that has closed its standard input.
type fakePiped struct {
	*fakeProcess
	errors      *io.PipeReader
	inputClosed chan struct{}
	refuseInput bool
}

func (p *fakePiped) Write(b []byte) (int, error) {
	if p.refuseInput {
		return 0, syscall.EPIPE
	}

	return p.fakeProcess.Write(b)
}

func (p *fakePiped) Stderr() io.Reader { return p.errors }

func (p *fakePiped) CloseInput() error {
	close(p.inputClosed)

	return nil
}

// execFixture joins a server and an Exec client over an in-memory
// connection.
type execFixture struct {
	process   *fakePiped
	prints    *io.PipeWriter
	errPrints *io.PipeWriter
	out       *syncBuffer
	errors    *syncBuffer
	served    chan error
	ran       chan attachResult
}

func newExecFixture(t *testing.T, input io.Reader, options ...func(*fakePiped)) *execFixture {
	t.Helper()

	output, prints := io.Pipe()
	errOutput, errPrints := io.Pipe()

	f := &execFixture{
		process: &fakePiped{
			fakeProcess: &fakeProcess{
				output:  output,
				typed:   &syncBuffer{},
				resized: make(chan session.Size, 10),
				exit:    make(chan int, 1),
				started: make(chan struct{}),
				waited:  make(chan struct{}),
				closed:  make(chan struct{}),
			},
			errors:      errOutput,
			inputClosed: make(chan struct{}),
		},
		prints:    prints,
		errPrints: errPrints,
		out:       &syncBuffer{},
		errors:    &syncBuffer{},
		served:    make(chan error, 1),
		ran:       make(chan attachResult, 1),
	}

	for _, option := range options {
		option(f.process)
	}

	var start session.Starter = func(request session.Request) (session.Process, error) {
		f.process.request = request
		close(f.process.started)

		return f.process, nil
	}

	client := session.Exec{In: input, Out: f.out, Errors: f.errors, Env: []string{"TASK_ID=42"}}

	guestSide, hostSide := connPair(t)

	go func() { f.served <- session.Serve(guestSide, start) }()

	go func() {
		code, err := client.Run(hostSide)
		f.ran <- attachResult{code, err}
	}()

	select {
	case <-f.process.started:
	case <-time.After(patience):
		t.Fatal("the process was not started")
	}

	return f
}

// end closes the output of the process, lets it exit with the code and
// returns what the client and the server came back with.
func (f *execFixture) end(t *testing.T, code int) (attachResult, error) {
	t.Helper()

	_ = f.prints.Close()
	_ = f.errPrints.Close()
	f.process.exit <- code

	var ran attachResult

	select {
	case ran = <-f.ran:
	case <-time.After(patience):
		t.Fatal("Run did not return")
	}

	select {
	case err := <-f.served:
		return ran, err
	case <-time.After(patience):
		t.Fatal("Serve did not return")

		return ran, nil
	}
}

func TestExecStartsTheProcessWithoutATerminal(t *testing.T) {
	// act
	f := newExecFixture(t, nil)

	// assert
	assert.Equal(t, session.Request{Env: []string{"TASK_ID=42"}}, f.process.request)
}

func TestExecSendsTheInputAndThenEndsIt(t *testing.T) {
	// act
	f := newExecFixture(t, strings.NewReader("fix the flaky test"))

	// assert
	select {
	case <-f.process.inputClosed:
	case <-time.After(patience):
		t.Fatal("the input was not closed")
	}

	assert.Equal(t, "fix the flaky test", f.process.typed.String())
}

func TestExecEndsTheInputAtOnceWithoutInput(t *testing.T) {
	// act
	f := newExecFixture(t, nil)

	// assert
	select {
	case <-f.process.inputClosed:
	case <-time.After(patience):
		t.Fatal("the input was not closed")
	}
}

func TestExecKeepsStandardErrorApart(t *testing.T) {
	// arrange
	f := newExecFixture(t, nil)

	go func() { _, _ = io.WriteString(f.prints, `{"type":"result"}`+"\n") }()
	go func() { _, _ = io.WriteString(f.errPrints, "a warning\n") }()

	// act
	assert.Eventually(t, func() bool { return f.out.String() != "" && f.errors.String() != "" }, 5*time.Second, 10*time.Millisecond)
	ran, served := f.end(t, 0)

	// assert
	require.NoError(t, served)
	require.NoError(t, ran.err)
	assert.Equal(t, `{"type":"result"}`+"\n", f.out.String())
	assert.Equal(t, "a warning\n", f.errors.String())
}

func TestExecDeliversOutputThatComesAfterTheExit(t *testing.T) {
	// arrange
	f := newExecFixture(t, nil)
	f.process.exit <- 3

	select {
	case <-f.process.waited:
	case <-time.After(patience):
		t.Fatal("Wait was not called")
	}

	// act
	_, err := io.WriteString(f.prints, "late line\n")
	require.NoError(t, err)
	_, err = io.WriteString(f.errPrints, "late warning\n")
	require.NoError(t, err)

	_ = f.prints.Close()
	_ = f.errPrints.Close()

	// assert
	select {
	case ran := <-f.ran:
		require.NoError(t, ran.err)
		assert.Equal(t, 3, ran.code)
	case <-time.After(patience):
		t.Fatal("Run did not return")
	}

	assert.Equal(t, "late line\n", f.out.String())
	assert.Equal(t, "late warning\n", f.errors.String())
}

func TestExecSucceedsWhenTheCommandEndsWithoutReadingAllInput(t *testing.T) {
	// arrange
	input := strings.NewReader(strings.Repeat("x", 8<<20))
	f := newExecFixture(t, input, func(p *fakePiped) { p.refuseInput = true })

	// act
	ran, served := f.end(t, 0)

	// assert
	require.NoError(t, served)
	require.NoError(t, ran.err)
	assert.Equal(t, 0, ran.code)
}
