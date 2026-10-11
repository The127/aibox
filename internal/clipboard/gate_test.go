package clipboard_test

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/clipboard"
)

// fakeClipboard holds image, or err, and counts how often it was read.
type fakeClipboard struct {
	image []byte
	err   error
	reads int
}

func (c *fakeClipboard) read() ([]byte, error) {
	c.reads++

	return c.image, c.err
}

func get(gate *clipboard.Gate, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	gate.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

	return recorder
}

func TestTheGateRefusesTheVMBeforeCtrlV(t *testing.T) {
	for _, path := range []string{clipboard.TypesPath, clipboard.ImagePath} {
		t.Run(path, func(t *testing.T) {
			// arrange
			c := &fakeClipboard{image: []byte("the image")}
			gate := &clipboard.Gate{Read: c.read}

			// act
			response := get(gate, path)

			// assert
			assert.Equal(t, http.StatusForbidden, response.Code)
			assert.Zero(t, c.reads)
		})
	}
}

func TestTheGateGivesTheTypesAndThenTheImageAfterCtrlV(t *testing.T) {
	// arrange
	c := &fakeClipboard{image: []byte("the image")}
	gate := &clipboard.Gate{Read: c.read}
	gate.Arm()

	// act
	types := get(gate, clipboard.TypesPath)
	image := get(gate, clipboard.ImagePath)

	// assert
	assert.Equal(t, http.StatusOK, types.Code)
	assert.Equal(t, "image/png\n", types.Body.String())
	assert.Equal(t, http.StatusOK, image.Code)
	assert.Equal(t, "image/png", image.Header().Get("Content-Type"))
	assert.Equal(t, "the image", image.Body.String())
	assert.Equal(t, 1, c.reads, "the image is the one the types were read from")
}

func TestTheGateGivesTheImageOnceForEachCtrlV(t *testing.T) {
	// arrange
	c := &fakeClipboard{image: []byte("the image")}
	gate := &clipboard.Gate{Read: c.read}
	gate.Arm()

	// act
	first := get(gate, clipboard.ImagePath)
	second := get(gate, clipboard.ImagePath)
	types := get(gate, clipboard.TypesPath)

	gate.Arm()
	again := get(gate, clipboard.ImagePath)

	// assert
	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, http.StatusForbidden, second.Code)
	assert.Equal(t, http.StatusForbidden, types.Code)
	assert.Equal(t, http.StatusOK, again.Code)
	assert.Equal(t, 2, c.reads)
}

func TestTheGateClosesWhenTheWindowEnds(t *testing.T) {
	// arrange
	c := &fakeClipboard{image: []byte("the image")}
	gate := &clipboard.Gate{Read: c.read, Window: 50 * time.Millisecond}
	gate.Arm()

	// act
	types := get(gate, clipboard.TypesPath)

	time.Sleep(100 * time.Millisecond)

	image := get(gate, clipboard.ImagePath)

	// assert
	assert.Equal(t, http.StatusOK, types.Code)
	assert.Equal(t, http.StatusForbidden, image.Code)
}

func TestTheGateReadsTheClipboardAgainAfterTheNextCtrlV(t *testing.T) {
	// arrange
	c := &fakeClipboard{image: []byte("the old image")}
	gate := &clipboard.Gate{Read: c.read}
	gate.Arm()
	get(gate, clipboard.TypesPath)

	// act
	c.image = []byte("the new image")
	gate.Arm()
	image := get(gate, clipboard.ImagePath)

	// assert
	assert.Equal(t, "the new image", image.Body.String())
}

func TestTheGateSaysWhenTheClipboardHoldsNoImage(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"no image":      {fmt.Errorf("%w: xclip: target image/png not available", clipboard.ErrNoImage), http.StatusNotFound},
		"broken helper": {errors.New("read the answer of the clipboard helper: EOF"), http.StatusInternalServerError},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			c := &fakeClipboard{err: test.err}

			var log bytes.Buffer

			gate := &clipboard.Gate{Read: c.read, Log: &log}
			gate.Arm()

			// act
			response := get(gate, clipboard.TypesPath)

			// assert
			assert.Equal(t, test.want, response.Code)
			assert.Contains(t, log.String(), test.err.Error())
		})
	}
}

func TestTheGateClosesWhenTheClipboardHoldsNoImage(t *testing.T) {
	// arrange
	c := &fakeClipboard{err: clipboard.ErrNoImage}
	gate := &clipboard.Gate{Read: c.read}
	gate.Arm()
	get(gate, clipboard.TypesPath)

	// act
	c.image, c.err = []byte("the image"), nil
	again := get(gate, clipboard.TypesPath)

	// assert
	assert.Equal(t, http.StatusForbidden, again.Code)
	assert.Equal(t, 1, c.reads)
}

func TestTheGateKeepsCtrlVThatCameDuringARead(t *testing.T) {
	// arrange
	reading, finish := make(chan struct{}), make(chan struct{})
	reads := 0
	gate := &clipboard.Gate{Read: func() ([]byte, error) {
		reads++
		if reads == 1 {
			close(reading)
			<-finish
		}

		return fmt.Appendf(nil, "image %d", reads), nil
	}}
	gate.Arm()

	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- get(gate, clipboard.ImagePath) }()

	<-reading

	// act
	gate.Arm()
	close(finish)

	old := <-first
	next := get(gate, clipboard.ImagePath)

	// assert
	assert.Equal(t, "image 1", old.Body.String())
	assert.Equal(t, http.StatusOK, next.Code)
	assert.Equal(t, "image 2", next.Body.String())
}

func TestTheGateServesNothingElse(t *testing.T) {
	// arrange
	c := &fakeClipboard{image: []byte("the image")}
	gate := &clipboard.Gate{Read: c.read}
	gate.Arm()

	// act
	other := get(gate, "/text")

	post := httptest.NewRecorder()
	gate.ServeHTTP(post, httptest.NewRequest(http.MethodPost, clipboard.ImagePath, nil))

	// assert
	assert.Equal(t, http.StatusNotFound, other.Code)
	assert.Equal(t, http.StatusNotFound, post.Code)
	assert.Zero(t, c.reads)

	image := get(gate, clipboard.ImagePath)
	require.Equal(t, http.StatusOK, image.Code)
}
