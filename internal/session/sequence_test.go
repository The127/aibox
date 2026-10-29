package session_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/session"
)

// printer is a command that prints its text and exits with its code.
type printer struct {
	*strings.Reader
	code int
}

func (printer) Write(b []byte) (int, error) { return len(b), nil }
func (printer) Resize(session.Size) error   { return nil }
func (p printer) Wait() (int, error)        { return p.code, nil }
func (printer) Close() error                { return nil }

func prints(text string, code int) session.Starter {
	return func(session.Request) (session.Process, error) {
		return printer{strings.NewReader(text), code}, nil
	}
}

func waitFor(t *testing.T, served <-chan error) error {
	t.Helper()

	select {
	case err := <-served:
		return err
	case <-time.After(patience):
		t.Fatal("Serve did not return")

		return nil
	}
}

func TestASecondSessionRunsOnTheConnectionOnceTheFirstEnded(t *testing.T) {
	// arrange
	guestSide, hostSide := connPair(t)
	served := make(chan error, 1)

	go func() { served <- session.Serve(guestSide, prints("on the terminal", 3), prints("results", 0)) }()

	conn, err := session.Connect(hostSide, nil)
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	var screen, results syncBuffer

	code, err := session.Client{In: strings.NewReader(""), Out: &screen}.Attach(conn)
	require.NoError(t, err)

	// act
	resultsCode, err := session.Exec{Out: &results, Errors: io.Discard}.Run(conn)

	// assert
	require.NoError(t, err)
	assert.Equal(t, 3, code)
	assert.Equal(t, "on the terminal", screen.String())
	assert.Equal(t, 0, resultsCode)
	assert.Equal(t, "results", results.String())
	require.NoError(t, waitFor(t, served))
}

func TestASessionBeyondTheLastStarterIsRefused(t *testing.T) {
	// arrange
	guestSide, hostSide := connPair(t)

	go func() { _ = session.Serve(guestSide, prints("on the terminal", 0)) }()

	conn, err := session.Connect(hostSide, nil)
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	_, err = session.Client{In: strings.NewReader(""), Out: io.Discard}.Attach(conn)
	require.NoError(t, err)

	// act
	_, err = session.Exec{Out: io.Discard, Errors: io.Discard}.Run(conn)

	// assert
	assert.ErrorContains(t, err, "open the session")
}

func TestServeFailsWhenTheClientLeavesBeforeTheLastSession(t *testing.T) {
	// arrange
	guestSide, hostSide := connPair(t)
	served := make(chan error, 1)

	go func() { served <- session.Serve(guestSide, prints("on the terminal", 0), prints("results", 0)) }()

	conn, err := session.Connect(hostSide, nil)
	require.NoError(t, err)

	_, err = session.Client{In: strings.NewReader(""), Out: io.Discard}.Attach(conn)
	require.NoError(t, err)

	// act
	_ = conn.Close()

	// assert
	assert.ErrorContains(t, waitFor(t, served), "no session")
}

func TestAForwardKeepsWorkingInTheSecondSession(t *testing.T) {
	// arrange
	port := echoServer(t)
	guestSide, hostSide := connPair(t)
	exit, started := make(chan struct{}), make(chan struct{})

	var secondStart session.Starter = func(session.Request) (session.Process, error) {
		close(started)

		return &waiting{exit: exit}, nil
	}

	go func() { _ = session.Serve(guestSide, prints("on the terminal", 0), secondStart) }()

	dials := make(chan session.Dial, 1)

	conn, err := session.Connect(hostSide, func(ctx context.Context, dial session.Dial) {
		dials <- dial
		<-ctx.Done()
	})
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	_, err = session.Client{In: strings.NewReader(""), Out: io.Discard}.Attach(conn)
	require.NoError(t, err)

	ran := make(chan error, 1)

	go func() {
		_, err := session.Exec{Out: io.Discard, Errors: io.Discard}.Run(conn)
		ran <- err
	}()

	<-started

	// act
	echo, err := (<-dials)(port)
	require.NoError(t, err)

	defer func() { _ = echo.Close() }()

	_, err = io.WriteString(echo, "ping")
	require.NoError(t, err)
	require.NoError(t, echo.(interface{ CloseWrite() error }).CloseWrite())

	answer, err := io.ReadAll(echo)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "ping", string(answer))

	close(exit)
	require.NoError(t, <-ran)
}

func TestASecondSessionOpenedEarlyWaitsForTheFirst(t *testing.T) {
	// arrange
	guestSide, hostSide := connPair(t)
	exit, started := make(chan struct{}), make(chan struct{})
	served := make(chan error, 1)

	var firstStart session.Starter = func(session.Request) (session.Process, error) {
		close(started)

		return &waiting{exit: exit}, nil
	}

	go func() { served <- session.Serve(guestSide, firstStart, prints("results", 0)) }()

	conn, err := session.Connect(hostSide, nil)
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	attached := make(chan error, 1)

	go func() {
		_, err := session.Client{In: strings.NewReader(""), Out: io.Discard}.Attach(conn)
		attached <- err
	}()

	<-started

	var results syncBuffer

	ran := make(chan error, 1)

	go func() {
		_, err := session.Exec{Out: &results, Errors: io.Discard}.Run(conn)
		ran <- err
	}()

	// act
	select {
	case <-ran:
		t.Fatal("the second session ran while the first one did")
	case <-time.After(100 * time.Millisecond):
	}

	close(exit)

	// assert
	require.NoError(t, <-attached)
	require.NoError(t, <-ran)
	assert.Equal(t, "results", results.String())
	require.NoError(t, waitFor(t, served))
}

// waiting is a command that prints nothing and exits once exit is closed.
type waiting struct {
	exit chan struct{}
}

func (w *waiting) Read([]byte) (int, error) {
	<-w.exit

	return 0, io.EOF
}

func (*waiting) Write(b []byte) (int, error) { return len(b), nil }
func (*waiting) Resize(session.Size) error   { return nil }
func (*waiting) Wait() (int, error)          { return 0, nil }
func (*waiting) Close() error                { return nil }
