package session_test

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/session"
)

// presses reads typed through the watcher and returns what came through and
// how often it saw Ctrl+V.
func presses(t *testing.T, typed io.Reader) (string, int) {
	t.Helper()

	count := 0
	got, err := io.ReadAll(session.WatchPaste(typed, func() { count++ }))
	require.NoError(t, err)

	return string(got), count
}

func TestTheWatcherSeesCtrlV(t *testing.T) {
	for name, typed := range map[string]string{
		"control byte":     "\x16",
		"modifyOtherKeys":  "\x1b[27;5;118~",
		"kitty":            "\x1b[118;5u",
		"kitty, key down":  "\x1b[118;5:1u",
		"kitty, caps lock": "\x1b[118;69u",
		"kitty, num lock":  "\x1b[118;133:1u",
		"kitty, alternate": "\x1b[118:86;5u",
		"xterm, caps lock": "\x1b[27;69;118~",
		"between the keys": "ab\x16cd",
		"after alt+v":      "\x1bv\x16",
		"after escape":     "\x1b\x16",
		"after F1":         "\x1bOP\x16",
		"after an arrow":   "\x1b[A\x16",
	} {
		t.Run(name, func(t *testing.T) {
			// act
			got, count := presses(t, strings.NewReader(typed))

			// assert
			assert.Equal(t, typed, got)
			assert.Equal(t, 1, count)
		})
	}
}

func TestTheWatcherSeesCtrlVSplitAcrossReads(t *testing.T) {
	// act
	got, count := presses(t, iotest.OneByteReader(strings.NewReader("x\x1b[118;5uy\x1b[27;5;118~")))

	// assert
	assert.Equal(t, "x\x1b[118;5uy\x1b[27;5;118~", got)
	assert.Equal(t, 2, count)
}

func TestTheWatcherSeesNoCtrlVInPastedText(t *testing.T) {
	// act
	got, count := presses(t, strings.NewReader("\x1b[200~a\x16b\x1b[201~"))

	// assert
	assert.Equal(t, "\x1b[200~a\x16b\x1b[201~", got)
	assert.Zero(t, count)
}

func TestTheWatcherSeesCtrlVAfterPastedText(t *testing.T) {
	// act
	_, count := presses(t, iotest.OneByteReader(strings.NewReader("\x1b[200~a\x16b\x1b[201~\x16")))

	// assert
	assert.Equal(t, 1, count)
}

func TestTheWatcherSeesNoOtherKeys(t *testing.T) {
	for name, typed := range map[string]string{
		"text":                "hello",
		"ctrl+c":              "\x03",
		"kitty, v":            "\x1b[118u",
		"kitty, key up":       "\x1b[118;5:3u",
		"kitty, repeat":       "\x1b[118;5:2u",
		"kitty, ctrl+shift+v": "\x1b[118;6u",
		"a long sequence":     "\x1b[1;2;3;4;5;6;7;8;9;118;5u",
		"kitty, ctrl+alt+v":   "\x1b[118;7u",
		"modifyOtherKeys, v":  "\x1b[27;1;118~",
	} {
		t.Run(name, func(t *testing.T) {
			// act
			_, count := presses(t, strings.NewReader(typed))

			// assert
			assert.Zero(t, count)
		})
	}
}
