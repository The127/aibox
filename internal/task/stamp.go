package task

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Log writes the lines of a task to one writer, each after the time and a
// label with the ID of the task, so that the lines of tasks that share a
// log can be told apart. The text from the VM goes through a writer of its
// own label, and the VM cannot leave the label out, so a line from it
// cannot pass for a line of aibox.
type Log struct {
	mu    sync.Mutex
	w     io.Writer
	id    string
	now   func() time.Time
	width func() int
}

// NewLog returns a Log that writes to w, with the ID id and the time now
// gives.
func NewLog(w io.Writer, id string, now func() time.Time) *Log {
	return &Log{w: w, id: id, now: now}
}

// Wrap breaks each line into rows that fit the width the function returns
// when the line is written, and stamps every row. A terminal would break a
// longer line itself, and the VM could then fill a line so that a row
// without a stamp starts with text that looks like a line of aibox. A
// width of 0 or less breaks no line, for output that is no terminal.
func (l *Log) Wrap(width func() int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.width = width
}

// Writer returns a writer whose lines go to the log after label. A line
// that starts with the label and a colon loses them, since the stamp
// carries the label. The writers of a log may write at once, a line never
// splits another.
func (l *Log) Writer(label string) *LogWriter {
	return &LogWriter{log: l, label: label}
}

// maxLineBytes is how long a line of a LogWriter may get. The rest of a
// longer line is dropped.
const maxLineBytes = 4096

// LogWriter is a writer of a Log. It cleans each line with CleanText and
// takes a CRLF for a newline. Close writes what is left of the last line,
// and a write after Close goes out at once, so that none is lost.
type LogWriter struct {
	log    *Log
	label  string
	line   []byte
	closed bool
}

func (w *LogWriter) Write(b []byte) (int, error) {
	w.log.mu.Lock()
	defer w.log.mu.Unlock()

	n := len(b)

	for len(b) > 0 {
		part, rest, ended := bytes.Cut(b, []byte("\n"))
		w.line = append(w.line, part[:min(len(part), maxLineBytes-len(w.line))]...)

		if !ended {
			break
		}

		if err := w.flush(); err != nil {
			return 0, err
		}

		b = rest
	}

	if w.closed && len(w.line) > 0 {
		if err := w.flush(); err != nil {
			return 0, err
		}
	}

	return n, nil
}

// Close writes what is left of the last line.
func (w *LogWriter) Close() error {
	w.log.mu.Lock()
	defer w.log.mu.Unlock()

	w.closed = true

	if len(w.line) == 0 {
		return nil
	}

	return w.flush()
}

func (w *LogWriter) flush() error {
	text := CleanText(strings.TrimPrefix(strings.TrimSuffix(string(w.line), "\r"), w.label+": "))
	w.line = w.line[:0]

	stamp := fmt.Sprintf("%s %s[%s]: ", w.log.now().Format(time.TimeOnly), w.label, w.log.id)

	var b strings.Builder

	for _, row := range rows(text, w.log.columns(len(stamp))) {
		b.WriteString(stamp + row + "\n")
	}

	_, err := io.WriteString(w.log.w, b.String())

	return err
}

// minColumns is the fewest columns a row gets after its stamp. On a
// terminal too narrow for that, the terminal breaks the row, but the part
// it moves to the next row holds too little of the VM's text for a stamp.
const minColumns = 8

// columns is how many columns a row has after a stamp of stampWidth, or 0
// for rows of any length. The last column stays empty: some terminals
// break a line that fills the row.
func (l *Log) columns(stampWidth int) int {
	if l.width == nil {
		return 0
	}

	width := l.width()
	if width <= 0 {
		return 0
	}

	return max(width-stampWidth-1, minColumns)
}

// rows breaks the text into rows of at most columns columns, or returns it
// whole when columns is 0. A character other than ASCII counts as two
// columns, the most a terminal gives one, so that no row comes out wider
// than the terminal thinks.
func rows(text string, columns int) []string {
	if columns <= 0 {
		return []string{text}
	}

	var (
		broken []string
		start  int
		used   int
	)

	for i, r := range text {
		width := 2
		if r < utf8.RuneSelf {
			width = 1
		}

		if used+width > columns && i > start {
			broken = append(broken, text[start:i])
			start, used = i, 0
		}

		used += width
	}

	return append(broken, text[start:])
}
