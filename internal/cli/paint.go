package cli

import (
	"fmt"
	"io"
	"strings"
)

// style is an ANSI code for the look of text on a terminal.
type style string

const (
	bold   style = "1"
	dim    style = "2"
	cyan   style = "36"
	yellow style = "33"
	green  style = "32"
)

// defaultWidth is the width text is wrapped to when stderr is no terminal
// that says how wide it is.
const defaultWidth = 80

// painter writes text for people to stderr, in colors when stderr is a
// terminal and NO_COLOR is unset, as https://no-color.org asks.
type painter struct {
	w      io.Writer
	colors bool
	width  int
}

func newPainter(deps dependencies) painter {
	width, terminal := deps.stderrWidth()
	_, noColor := deps.lookupEnv("NO_COLOR")

	if width <= 0 {
		width = defaultWidth
	}

	return painter{w: deps.stderr, colors: terminal && !noColor, width: width}
}

func (p painter) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.w, format, args...)
}

// paint gives the text the styles, if the painter uses colors.
func (p painter) paint(text string, styles ...style) string {
	if !p.colors || len(styles) == 0 {
		return text
	}

	codes := make([]string, 0, len(styles))
	for _, s := range styles {
		codes = append(codes, string(s))
	}

	return "\x1b[" + strings.Join(codes, ";") + "m" + text + "\x1b[0m"
}

// each paints every text with the styles.
func (p painter) each(texts []string, styles ...style) []string {
	painted := make([]string, 0, len(texts))
	for _, text := range texts {
		painted = append(painted, p.paint(text, styles...))
	}

	return painted
}
