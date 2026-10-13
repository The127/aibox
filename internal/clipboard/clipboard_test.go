package clipboard_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/clipboard"
)

// startHelper runs the helper with read in place of the clipboard and
// returns the side aibox talks to it on.
func startHelper(t *testing.T, read func() ([]byte, error)) *clipboard.Helper {
	t.Helper()

	requests, ask := io.Pipe()
	answers, answer := io.Pipe()
	served := make(chan error, 1)

	go func() {
		served <- clipboard.ServeWith(requests, answer, read)
		_ = answer.Close()
	}()

	t.Cleanup(func() {
		_ = ask.Close()

		assert.NoError(t, <-served)
	})

	return clipboard.HelperOn(ask, answers)
}

func TestTheHelperSendsTheImage(t *testing.T) {
	// arrange
	image := []byte("\x89PNG\r\n\x1a\nthe rest")
	helper := startHelper(t, func() ([]byte, error) { return image, nil })

	// act
	got, err := helper.Read()

	// assert
	require.NoError(t, err)
	assert.Equal(t, image, got)
}

func TestTheHelperSaysWhyThereIsNoImage(t *testing.T) {
	// arrange
	helper := startHelper(t, func() ([]byte, error) { return nil, errors.New("xclip: target image/png not available") })

	// act
	_, err := helper.Read()

	// assert
	require.ErrorIs(t, err, clipboard.ErrNoImage)
	assert.ErrorContains(t, err, "xclip: target image/png not available")
}

func TestTheHelperAnswersEachRequest(t *testing.T) {
	// arrange
	reads := 0
	helper := startHelper(t, func() ([]byte, error) {
		reads++

		return fmt.Appendf(nil, "image %d", reads), nil
	})

	// act
	first, firstErr := helper.Read()
	second, secondErr := helper.Read()

	// assert
	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	assert.Equal(t, "image 1", string(first))
	assert.Equal(t, "image 2", string(second))
}

func TestReadFailsOnAnAnswerThatIsCutShort(t *testing.T) {
	// arrange
	requests, ask := io.Pipe()

	go func() { _, _ = io.Copy(io.Discard, requests) }()

	t.Cleanup(func() { _ = ask.Close() })

	// an image of 2^63 bytes, of which one arrives
	answer := strings.NewReader("\x00\x80\x00\x00\x00\x00\x00\x00\x00x")
	helper := clipboard.HelperOn(ask, answer)

	// act
	_, err := helper.Read()
	_, again := helper.Read()

	// assert
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.ErrorIs(t, again, io.ErrUnexpectedEOF, "the pipes are out of step, so the helper stays broken")
}

func TestTheHelperSendsAnEmptyImage(t *testing.T) {
	// arrange
	helper := startHelper(t, func() ([]byte, error) { return []byte{}, nil })

	// act
	got, err := helper.Read()

	// assert
	require.NoError(t, err)
	assert.Empty(t, got)
}

// startBothHelper is startHelper with a reader of the text too.
func startBothHelper(t *testing.T, read, readText func() ([]byte, error)) *clipboard.Helper {
	t.Helper()

	requests, ask := io.Pipe()
	answers, answer := io.Pipe()
	served := make(chan error, 1)

	go func() {
		served <- clipboard.ServeBothWith(requests, answer, read, readText)
		_ = answer.Close()
	}()

	t.Cleanup(func() {
		_ = ask.Close()

		assert.NoError(t, <-served)
	})

	return clipboard.HelperOn(ask, answers)
}

func TestTheHelperSendsTheTextOrTheImageAsAsked(t *testing.T) {
	// arrange
	helper := startBothHelper(t,
		func() ([]byte, error) { return []byte("\x89PNG"), nil },
		func() ([]byte, error) { return []byte("Grüße\r\naus Wien"), nil })

	// act
	text, textErr := helper.ReadText()
	image, imageErr := helper.Read()

	// assert
	require.NoError(t, textErr)
	require.NoError(t, imageErr)
	assert.Equal(t, "Grüße\r\naus Wien", text)
	assert.Equal(t, []byte("\x89PNG"), image)
}

func TestTheHelperSaysWhyThereIsNoText(t *testing.T) {
	// arrange
	helper := startBothHelper(t,
		func() ([]byte, error) { return nil, errors.New("no image") },
		func() ([]byte, error) { return nil, errors.New("no text") })

	// act
	_, err := helper.ReadText()

	// assert
	require.ErrorIs(t, err, clipboard.ErrNoText)
	assert.ErrorContains(t, err, "no text")
}
