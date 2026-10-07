package task

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The sizes the results may have. The VM cuts the transcript and the log
// at theirs and refuses changes above theirs, the host refuses anything
// larger.
const (
	MaxTranscriptBytes = 256 << 20
	MaxLogBytes        = 1 << 20
	MaxChangesBytes    = 1 << 30
	MaxResultBytes     = 1 << 20
)

var (
	// ErrBadResults is an archive of results the host does not take.
	ErrBadResults = errors.New("the VM sent results aibox does not take")
	// ErrBadBundle is a bundle of changes the host does not take.
	ErrBadBundle = errors.New("the bundle of changes is not one aibox takes")
)

// limits are the results by name and the size each may have.
var limits = map[string]int64{
	TranscriptFile: MaxTranscriptBytes,
	LogFile:        MaxLogBytes,
	ChangesFile:    MaxChangesBytes,
	ResultFile:     MaxResultBytes,
}

// Receive reads the archive of results and writes each file to the writer
// of its name. It refuses a file it does not know, one that is not a plain
// file, too large or sent twice, and anything after ResultFile, and it
// needs ResultFile.
func Receive(r io.Reader, files map[string]io.Writer) error {
	archive := tar.NewReader(r)
	seen := map[string]bool{}

	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			if !seen[ResultFile] {
				return fmt.Errorf("%w: no %s", ErrBadResults, ResultFile)
			}

			return nil
		}

		if err != nil {
			return fmt.Errorf("%w: %w", ErrBadResults, err)
		}

		limit, known := limits[header.Name]
		w, wanted := files[header.Name]

		switch {
		case !known || !wanted:
			return fmt.Errorf("%w: a file named %q", ErrBadResults, CleanText(header.Name))
		case header.Typeflag != tar.TypeReg:
			return fmt.Errorf("%w: %s is not a plain file", ErrBadResults, header.Name)
		case seen[header.Name]:
			return fmt.Errorf("%w: %s twice", ErrBadResults, header.Name)
		case seen[ResultFile]:
			return fmt.Errorf("%w: %s after %s", ErrBadResults, header.Name, ResultFile)
		case header.Size > limit:
			return fmt.Errorf("%w: %s has %d bytes, more than %d", ErrBadResults, header.Name, header.Size, limit)
		}

		seen[header.Name] = true

		if _, err := io.CopyN(w, archive, header.Size); err != nil {
			return fmt.Errorf("write %s: %w", header.Name, err)
		}
	}
}

// Bundle is the header of a git bundle: the commits it needs and the refs
// it carries, by name.
type Bundle struct {
	Prerequisites []string
	Refs          map[string]string
}

var (
	bundleSignatures = []string{"# v2 git bundle", "# v3 git bundle"}
	capabilities     = []string{"@object-format=sha1", "@object-format=sha256"}
	objectName       = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// maxHeaderLines is how many lines the header of a bundle may have. The
// VM sends one ref, and a few prerequisites at most.
const maxHeaderLines = 1024

// ReadBundle reads the header of a git bundle without git, so that the
// host knows what it holds before git looks at it.
func ReadBundle(r io.Reader) (Bundle, error) {
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 4096), 4096)

	bundle := Bundle{Refs: map[string]string{}}

	if !lines.Scan() || !slices.Contains(bundleSignatures, lines.Text()) {
		return Bundle{}, fmt.Errorf("%w: it has no bundle signature", ErrBadBundle)
	}

	for range maxHeaderLines {
		if !lines.Scan() {
			return Bundle{}, fmt.Errorf("%w: its header does not end: %w", ErrBadBundle, errors.Join(lines.Err(), io.ErrUnexpectedEOF))
		}

		line := lines.Text()

		switch {
		case line == "":
			return bundle, nil
		case strings.HasPrefix(line, "@"):
			// a v3 bundle names its hash, and git would take a bundle with
			// other capabilities, such as a filter, for something else
			if !slices.Contains(capabilities, line) {
				return Bundle{}, fmt.Errorf("%w: a capability %q", ErrBadBundle, CleanText(line))
			}
		case strings.HasPrefix(line, "-"):
			name, _, _ := strings.Cut(line[1:], " ")
			if !objectName.MatchString(name) {
				return Bundle{}, fmt.Errorf("%w: a prerequisite %q", ErrBadBundle, CleanText(line))
			}

			bundle.Prerequisites = append(bundle.Prerequisites, name)
		default:
			name, ref, ok := strings.Cut(line, " ")
			if !ok || !objectName.MatchString(name) || ref == "" {
				return Bundle{}, fmt.Errorf("%w: a line %q", ErrBadBundle, CleanText(line))
			}

			if _, twice := bundle.Refs[ref]; twice {
				return Bundle{}, fmt.Errorf("%w: %q twice", ErrBadBundle, CleanText(ref))
			}

			bundle.Refs[ref] = name
		}
	}

	return Bundle{}, fmt.Errorf("%w: its header has more than %d lines", ErrBadBundle, maxHeaderLines)
}

// Check returns ErrBadBundle unless the bundle carries Branch alone and
// needs no commit but base, the one the task started from. It returns the
// commit of Branch.
func (b Bundle) Check(base string) (string, error) {
	head, ok := b.Refs[Branch]
	if !ok || len(b.Refs) != 1 {
		return "", fmt.Errorf("%w: it must carry %s and nothing else", ErrBadBundle, Branch)
	}

	for _, prerequisite := range b.Prerequisites {
		if prerequisite != base {
			return "", fmt.Errorf("%w: it needs %s, not only the commit the task started from", ErrBadBundle, prerequisite)
		}
	}

	return head, nil
}

// CleanText makes text from the VM safe to show on a terminal: control and
// format characters but the newline become ?, and so do bytes that are no
// UTF-8.
func CleanText(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}

		if r == '\t' {
			return ' '
		}

		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || r == utf8.RuneError {
			return '?'
		}

		return r
	}, strings.ToValidUTF8(text, "?"))
}

// CleanLine is CleanText on one line: the lines of the text are joined
// with " | ", so that text from the VM cannot pass for a line of aibox.
func CleanLine(text string) string {
	return strings.Join(strings.Split(CleanText(strings.TrimSpace(text)), "\n"), " | ")
}

// Indent is CleanText with every line indented, for text from the VM that
// has lines of its own.
func Indent(text, prefix string) string {
	lines := strings.Split(CleanText(strings.TrimSpace(text)), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}

	return strings.Join(lines, "\n")
}

// maxLineBytes is how long a line of a LineCleaner may get. The rest of a
// longer line is dropped.
const maxLineBytes = 4096

// LineCleaner passes text on line by line, each cleaned with CleanText.
// Close passes on a last line without its newline.
type LineCleaner struct {
	w    io.Writer
	line []byte
}

// NewLineCleaner returns a LineCleaner that writes to w.
func NewLineCleaner(w io.Writer) *LineCleaner {
	return &LineCleaner{w: w}
}

func (c *LineCleaner) Write(b []byte) (int, error) {
	n := len(b)

	for len(b) > 0 {
		part, rest, ended := bytes.Cut(b, []byte("\n"))
		c.line = append(c.line, part[:min(len(part), maxLineBytes-len(c.line))]...)

		if !ended {
			break
		}

		if err := c.flush(); err != nil {
			return 0, err
		}

		b = rest
	}

	return n, nil
}

func (c *LineCleaner) flush() error {
	line := CleanText(string(c.line)) + "\n"
	c.line = c.line[:0]

	_, err := io.WriteString(c.w, line)

	return err
}

// Close writes what is left of the last line.
func (c *LineCleaner) Close() error {
	if len(c.line) == 0 {
		return nil
	}

	return c.flush()
}
