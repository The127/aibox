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

// altPresses reads typed through the watcher of Windows and returns what
// came through and how often it saw Ctrl+V or Alt+V.
func altPresses(t *testing.T, typed io.Reader) (string, int) {
	t.Helper()

	count := 0
	got, err := io.ReadAll(session.WatchPasteAltV(typed, func() { count++ }))
	require.NoError(t, err)

	return string(got), count
}

func TestTheWatcherOfWindowsPassesAltVOnAsCtrlV(t *testing.T) {
	for name, tt := range map[string]struct{ typed, want string }{
		"alt+v":                 {typed: "\x1bv", want: "\x16"},
		"kitty":                 {typed: "\x1b[118;3u", want: "\x16"},
		"kitty, key down":       {typed: "\x1b[118;3:1u", want: "\x16"},
		"kitty, num lock":       {typed: "\x1b[118;131u", want: "\x16"},
		"modifyOtherKeys":       {typed: "\x1b[27;3;118~", want: "\x16"},
		"between the keys":      {typed: "ab\x1bvcd", want: "ab\x16cd"},
		"after an arrow":        {typed: "\x1b[A\x1bv", want: "\x1b[A\x16"},
		"after escape":          {typed: "\x1b\x1bv", want: "\x1b\x16"},
		"twice":                 {typed: "\x1bv\x1b[118;3u", want: "\x16\x16"},
		"ctrl+v as it is":       {typed: "\x16", want: "\x16"},
		"after pasted text":     {typed: "\x1b[200~a\x1b[201~\x1bv", want: "\x1b[200~a\x1b[201~\x16"},
		"after a long sequence": {typed: "\x1b[1;2;3;4;5;6;7;8;9;118;5u\x1bv", want: "\x1b[1;2;3;4;5;6;7;8;9;118;5u\x16"},
	} {
		t.Run(name, func(t *testing.T) {
			// act
			got, count := altPresses(t, strings.NewReader(tt.typed))

			// assert
			assert.Equal(t, tt.want, got)
			assert.Equal(t, strings.Count(tt.want, "\x16"), count)
		})
	}
}

func TestTheWatcherOfWindowsPassesOtherAltKeysOn(t *testing.T) {
	for name, typed := range map[string]string{
		"alt+shift+v":       "\x1bV",
		"alt+x":             "\x1bx",
		"kitty, alt+v up":   "\x1b[118;3:3u",
		"kitty, ctrl+alt+v": "\x1b[118;7u",
		"in pasted text":    "\x1b[200~a\x1bvb\x1b[201~",
		"escape alone":      "\x1b",
	} {
		t.Run(name, func(t *testing.T) {
			// act
			got, count := altPresses(t, strings.NewReader(typed))

			// assert
			assert.Equal(t, typed, got)
			assert.Zero(t, count)
		})
	}
}

func TestTheWatcherOfWindowsPassesOnAnAltVSplitAcrossReads(t *testing.T) {
	// act: the escape held back until the next read would hold back the
	// Escape key too
	got, count := altPresses(t, iotest.OneByteReader(strings.NewReader("\x1bv")))

	// assert
	assert.Equal(t, "\x1bv", got)
	assert.Zero(t, count)
}

func TestTheWatcherOfLinuxAndMacOSPassesAltVOn(t *testing.T) {
	// act
	got, count := presses(t, strings.NewReader("\x1bv\x1b[118;3u"))

	// assert
	assert.Equal(t, "\x1bv\x1b[118;3u", got)
	assert.Zero(t, count)
}

func TestTheWatcherPastesInPlaceOfTheKey(t *testing.T) {
	for name, tt := range map[string]struct {
		typed string
		altV  bool
		want  string
	}{
		"ctrl+v":                  {typed: "a\x16b", want: "a<paste>b"},
		"kitty ctrl+v":            {typed: "a\x1b[118;5ub", want: "a<paste>b"},
		"alt+v when it pastes":    {typed: "a\x1bvb", altV: true, want: "a<paste>b"},
		"alt+v when it does not":  {typed: "a\x1bvb", want: "a\x1bvb"},
		"in pasted text, nothing": {typed: "\x1b[200~\x16\x1b[201~", want: "\x1b[200~\x16\x1b[201~"},
	} {
		t.Run(name, func(t *testing.T) {
			// act
			got, err := io.ReadAll(session.WatchPasteWith(strings.NewReader(tt.typed), func() []byte { return []byte("<paste>") }, tt.altV))

			// assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestTheWatcherPassesTheKeyOnWithoutText(t *testing.T) {
	// act: an image on the clipboard, which the VM reads itself
	got, err := io.ReadAll(session.WatchPasteWith(strings.NewReader("a\x16b\x1bv"), func() []byte { return nil }, true))

	// assert
	require.NoError(t, err)
	assert.Equal(t, "a\x16b\x16", string(got))
}

func TestTheWatcherHandsOutAPasteLongerThanTheReadsOfItsReader(t *testing.T) {
	// arrange
	long := strings.Repeat("0123456789", 100)
	r := session.WatchPasteWith(strings.NewReader("a\x16b"), func() []byte { return []byte(long) }, false)

	// act: as small reads as anyone may make
	var got []byte

	b := make([]byte, 3)

	for {
		n, err := r.Read(b)
		got = append(got, b[:n]...)

		if err != nil {
			require.ErrorIs(t, err, io.EOF)

			break
		}
	}

	// assert
	assert.Equal(t, "a"+long+"b", string(got))
}

func TestTheWatcherPassesOnAKeySplitAcrossReads(t *testing.T) {
	// act: what began the key went on with an earlier read
	got, err := io.ReadAll(session.WatchPasteWith(iotest.OneByteReader(strings.NewReader("\x1b[118;5u")), func() []byte { return []byte("<paste>") }, false))

	// assert
	require.NoError(t, err)
	assert.Equal(t, "\x1b[118;5u", string(got))
}

func TestPastedIsTheTextAsATerminalPastesIt(t *testing.T) {
	for name, tt := range map[string]struct {
		text      string
		bracketed bool
		want      string
	}{
		"lines end in carriage returns": {text: "a\r\nb\nc", want: "a\rb\rc"},
		"between the markers":           {text: "a", bracketed: true, want: "\x1b[200~a\x1b[201~"},
		"without escapes or controls":   {text: "a\x1b[201~rm -rf /\x07\x00b\u009b", bracketed: true, want: "\x1b[200~a[201~rm -rf /b\x1b[201~"},
		"with tabs and umlauts":         {text: "Grüße\tWien", want: "Grüße\tWien"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, string(session.Pasted(tt.text, tt.bracketed)))
		})
	}
}

func TestTheModeWatcherFollowsBracketedPasteAcrossWrites(t *testing.T) {
	// arrange
	var screen strings.Builder

	w, on := session.WatchMode(&screen)

	// act and assert: on, split across two writes, then off
	_, _ = io.WriteString(w, "text\x1b[?20")
	assert.False(t, on())

	_, _ = io.WriteString(w, "04hmore")
	assert.True(t, on())

	_, _ = io.WriteString(w, "\x1b[?2004l")
	assert.False(t, on())
	assert.Equal(t, "text\x1b[?2004hmore\x1b[?2004l", screen.String())
}
