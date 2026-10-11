//go:build linux

package guest_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/guest"
)

// fakeHost serves the clipboard of the host and records the paths asked
// for. With refuse it answers 403, as before Ctrl+V.
func fakeHost(t *testing.T, refuse bool) (string, *[]string) {
	t.Helper()

	var asked []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)

		if refuse {
			http.Error(w, "the VM may read the clipboard only right after you press Ctrl+V", http.StatusForbidden)

			return
		}

		switch r.URL.Path {
		case "/types":
			_, _ = w.Write([]byte("image/png\n"))
		case "/image.png":
			_, _ = w.Write([]byte("the image"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	return server.URL, &asked
}

func TestTheClipboardToolAsksTheHostWhatClaudeCodeAsks(t *testing.T) {
	tests := map[string]struct {
		args []string
		path string
		out  string
	}{
		"xclip types":       {[]string{"/usr/bin/xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"}, "/types", "image/png\n"},
		"xclip image":       {[]string{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"}, "/image.png", "the image"},
		"wl-paste types":    {[]string{"wl-paste", "-l"}, "/types", "image/png\n"},
		"wl-paste image":    {[]string{"/usr/bin/wl-paste", "--type", "image/png"}, "/image.png", "the image"},
		"wl-paste long -l":  {[]string{"wl-paste", "--list-types"}, "/types", "image/png\n"},
		"wl-paste short -t": {[]string{"wl-paste", "-t", "image/png"}, "/image.png", "the image"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			base, asked := fakeHost(t, false)

			var stdout, stderr bytes.Buffer

			// act
			code := guest.ClipboardToolAt(base, test.args, &stdout, &stderr)

			// assert
			assert.Zero(t, code, stderr.String())
			assert.Equal(t, []string{test.path}, *asked)
			assert.Equal(t, test.out, stdout.String())
		})
	}
}

func TestTheClipboardToolRefusesWhatClaudeCodeDoesNotAsk(t *testing.T) {
	tests := map[string][]string{
		"xclip text":           {"xclip", "-selection", "clipboard", "-o"},
		"xclip text/plain":     {"xclip", "-selection", "clipboard", "-t", "text/plain", "-o"},
		"xclip primary":        {"xclip", "-selection", "primary", "-t", "image/png", "-o"},
		"xclip bmp":            {"xclip", "-selection", "clipboard", "-t", "image/bmp", "-o"},
		"xclip copy":           {"xclip", "-selection", "clipboard", "-t", "image/png"},
		"xclip unknown flag":   {"xclip", "-selection", "clipboard", "-t", "image/png", "-o", "-loops", "2"},
		"wl-paste text":        {"wl-paste", "--no-newline"},
		"wl-paste primary":     {"wl-paste", "--primary", "--no-newline"},
		"wl-paste bmp":         {"wl-paste", "--type", "image/bmp"},
		"wl-paste list a type": {"wl-paste", "-l", "--type", "image/png"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			base, asked := fakeHost(t, false)

			var stdout, stderr bytes.Buffer

			// act
			code := guest.ClipboardToolAt(base, args, &stdout, &stderr)

			// assert
			assert.Equal(t, 1, code)
			assert.Empty(t, *asked)
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), "Ctrl+V")
		})
	}
}

func TestTheClipboardToolFailsWithWhatTheHostSays(t *testing.T) {
	// arrange
	base, _ := fakeHost(t, true)

	var stdout, stderr bytes.Buffer

	// act
	code := guest.ClipboardToolAt(base, []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"}, &stdout, &stderr)

	// assert
	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())
	assert.Equal(t, "xclip: the VM may read the clipboard only right after you press Ctrl+V\n", stderr.String())
}

func TestTheClipboardToolFailsWithoutTheHost(t *testing.T) {
	// arrange
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()

	var stdout, stderr bytes.Buffer

	// act
	code := guest.ClipboardToolAt(server.URL, []string{"wl-paste", "-l"}, &stdout, &stderr)

	// assert
	assert.Equal(t, 1, code)
	assert.True(t, strings.HasPrefix(stderr.String(), "wl-paste: "))
}

func TestIsClipboardTool(t *testing.T) {
	assert.True(t, guest.IsClipboardTool([]string{"/usr/bin/xclip"}))
	assert.True(t, guest.IsClipboardTool([]string{"wl-paste", "-l"}))
	assert.False(t, guest.IsClipboardTool([]string{"/sbin/init"}))
	assert.False(t, guest.IsClipboardTool(nil))
}
