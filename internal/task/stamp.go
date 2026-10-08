package task

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Log writes the lines of a task to one writer, each after the time and a
// label with the ID of the task, so that the lines of tasks that share a
// log can be told apart. The text from the VM goes through a writer of its
// own label, and the VM cannot leave the label out, so a line from it
// cannot pass for a line of aibox.
type Log struct {
	mu  sync.Mutex
	w   io.Writer
	id  string
	now func() time.Time
}

// NewLog returns a Log that writes to w, with the ID id and the time now
// gives.
func NewLog(w io.Writer, id string, now func() time.Time) *Log {
	return &Log{w: w, id: id, now: now}
}

// Writer returns a writer whose lines go to the log after label. A line
// that starts with the label and a colon loses them, since the stamp
// carries the label. The writers of a log may write at once, a line never
// splits another.
func (l *Log) Writer(label string) *LogWriter {
	return &LogWriter{log: l, label: label}
}

// LogWriter is a writer of a Log. Close writes what is left of the last
// line, and a write after Close goes out at once, so that none is lost.
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
	text := strings.TrimPrefix(string(w.line), w.label+": ")
	w.line = w.line[:0]

	_, err := fmt.Fprintf(w.log.w, "%s %s[%s]: %s\n", w.log.now().Format(time.TimeOnly), w.label, w.log.id, text)

	return err
}
