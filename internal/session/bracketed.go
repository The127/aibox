package session

import (
	"bytes"
	"io"
	"strings"
	"sync/atomic"
)

// The sequences a program turns bracketed paste on and off with, as a
// terminal takes them.
const (
	bracketedOn  = "\x1b[?2004h"
	bracketedOff = "\x1b[?2004l"
)

// modeWatcher passes on what the command prints and keeps whether it last
// turned bracketed paste on, which only a program that reads it does: to
// any other the markers would be keys.
type modeWatcher struct {
	w  io.Writer
	on *atomic.Bool
	// tail is the end of what came before, which may hold the start of a
	// sequence the next write ends
	tail []byte
}

func (m *modeWatcher) Write(b []byte) (int, error) {
	// a sequence that starts in the tail ends in the first bytes of b, and
	// comes before any b holds whole
	edge := make([]byte, 0, len(m.tail)+len(bracketedOn))
	edge = append(edge, m.tail...)
	edge = append(edge, b[:min(len(b), len(bracketedOn)-1)]...)

	for _, part := range [][]byte{edge, b} {
		on, off := bytes.LastIndex(part, []byte(bracketedOn)), bytes.LastIndex(part, []byte(bracketedOff))
		if on >= 0 || off >= 0 {
			m.on.Store(on > off)
		}
	}

	keep := len(bracketedOn) - 1
	if len(b) >= keep {
		m.tail = append(m.tail[:0], b[len(b)-keep:]...)
	} else {
		m.tail = append(m.tail[:0], edge[max(0, len(edge)-keep):]...)
	}

	return m.w.Write(b)
}

// pasted is the text as a terminal pastes it: lines that end in a carriage
// return, as the Enter key sends them, without the control characters,
// escapes above all, with which text could end the paste early and type
// keys of its own, and between the markers of bracketed paste when the
// program turned that on.
func pasted(text string, bracketed bool) []byte {
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")

	text = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\t' {
			return r
		}

		// C0, DEL and C1, which the terminal of the VM takes for controls
		if r < ' ' || (r >= 0x7f && r < 0xa0) {
			return -1
		}

		return r
	}, text)

	if !bracketed {
		return []byte(text)
	}

	return []byte("\x1b[200~" + text + "\x1b[201~")
}
