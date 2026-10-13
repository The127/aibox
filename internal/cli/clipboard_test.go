package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClipboard is the helper, with an image on the clipboard.
type fakeClipboard struct {
	started bool
	closed  bool
}

func (c *fakeClipboard) Read() ([]byte, error) { return []byte("the image"), nil }

func (c *fakeClipboard) ReadText() (string, error) { return "the text", nil }

func (c *fakeClipboard) Close() error {
	c.closed = true

	return nil
}

func TestRunSharesTheClipboardAfterCtrlV(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.True(t, f.clipboard.started)
	assert.True(t, f.clipboard.closed)

	spec := f.launch.spec
	assert.Equal(t, []uint16{3132}, spec.Loopback)
	require.NotNil(t, spec.Proxy.Local)

	handler := spec.Proxy.Local("localhost", "3132")
	require.NotNil(t, handler)

	before := httptest.NewRecorder()
	handler.ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/image.png", nil))
	assert.Equal(t, http.StatusForbidden, before.Code)

	require.NotNil(t, spec.OnPaste)
	spec.OnPaste()

	after := httptest.NewRecorder()
	handler.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/image.png", nil))
	assert.Equal(t, http.StatusOK, after.Code)
	assert.Equal(t, "the image", after.Body.String())
}

func TestRunSharesNoClipboardWhenTheConfigTurnsItOff(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "clipboard: false\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.False(t, f.clipboard.started)
	assert.Empty(t, f.launch.spec.Loopback)
	assert.Nil(t, f.launch.spec.Proxy.Local)
	assert.Nil(t, f.launch.spec.OnPaste)
}

func TestRunRefusesAnAllowListThatTakesThePortOfTheClipboard(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "allow:\n  - 127.0.0.1:3132\n")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorIs(t, err, errClipboardPort)
	assert.False(t, f.clipboard.started)
	assert.False(t, f.launch.called)
}

func TestRunLetsTheAllowListTakeThePortOfTheClipboardWhenItIsOff(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "clipboard: false\nallow:\n  - 127.0.0.1:3132\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []uint16{3132}, f.launch.spec.Loopback)
	assert.Nil(t, f.launch.spec.OnPaste)
}
