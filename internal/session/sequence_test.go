package session_test

import (
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
