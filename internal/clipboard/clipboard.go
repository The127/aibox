// Package clipboard lets the VM read an image from the clipboard of the
// host, once each time the person presses the paste key. A helper reads
// the clipboard, because aibox itself may start no programs once the VM
// runs.
package clipboard

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Command is the hidden command of aibox that runs the helper.
const Command = "clipboard"

// ErrNoText is a clipboard that holds no text, or text the helper could
// not read.
var ErrNoText = errors.New("the clipboard holds no text")

// ErrNoImage is a clipboard that holds no image, or one the helper could
// not read.
var ErrNoImage = errors.New("the clipboard holds no image")

// the first byte of each answer of the helper
const (
	answerData byte = 0
	answerNone byte = 1
)

// Helper is the helper process, started before aibox is confined.
type Helper struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	output io.Closer
	out    *bufio.Reader
	// mu keeps one request at a time on the pipes
	mu sync.Mutex
	// broken is why the pipes went out of step, after which nothing
	// read from them can be trusted
	broken error
}

// Start runs the helper as a child of aibox. It ends with Close, or when
// aibox ends and its input closes. What the helper prints on standard
// error goes to log.
func Start(log io.Writer) (*Helper, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find aibox for the clipboard helper: %w", err)
	}

	cmd := exec.Command(self, Command) //nolint:gosec // aibox starts itself with a fixed command
	cmd.Stderr = log

	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open the input of the clipboard helper: %w", err)
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open the output of the clipboard helper: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the clipboard helper: %w", err)
	}

	return &Helper{cmd: cmd, in: in, output: out, out: bufio.NewReader(out)}, nil
}

// Read asks the helper for the image on the clipboard, as PNG. It returns
// an error wrapping ErrNoImage when there is none.
func (h *Helper) Read() ([]byte, error) {
	return h.get("", ErrNoImage)
}

// ReadText asks the helper for the text on the clipboard, as UTF-8. It
// returns an error wrapping ErrNoText when there is none.
func (h *Helper) ReadText() (string, error) {
	text, err := h.get(textRequest, ErrNoText)

	return string(text), err
}

// get asks the helper with the request line, and wraps none in the
// helper's words when it has nothing.
func (h *Helper) get(request string, none error) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.broken != nil {
		return nil, h.broken
	}

	kind, data, err := h.ask(request)
	if err != nil {
		h.broken = fmt.Errorf("the clipboard helper broke: %w", err)

		return nil, h.broken
	}

	if kind != answerData {
		return nil, fmt.Errorf("%w: %s", none, data)
	}

	return data, nil
}

func (h *Helper) ask(request string) (byte, []byte, error) {
	if _, err := io.WriteString(h.in, request+"\n"); err != nil {
		return 0, nil, err
	}

	kind, err := h.out.ReadByte()
	if err != nil {
		return 0, nil, err
	}

	var size uint64
	if err := binary.Read(h.out, binary.BigEndian, &size); err != nil {
		return 0, nil, err
	}

	// read as it comes rather than trust the size for the memory up front
	data, err := io.ReadAll(io.LimitReader(h.out, int64(min(size, math.MaxInt64))))
	if err != nil {
		return 0, nil, err
	}

	if uint64(len(data)) != size {
		return 0, nil, io.ErrUnexpectedEOF
	}

	return kind, data, nil
}

// Close ends the helper and waits for it. A read the helper still runs
// ends within its timeout, and the answer fails on the closed output, so
// the helper does not leave the program that reads behind.
func (h *Helper) Close() error {
	_ = h.in.Close()
	_ = h.output.Close()

	err := h.cmd.Wait()

	// the helper fails to answer once its output is closed
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}

	return err
}

// textRequest is the line that asks the helper for the text on the
// clipboard. An empty line asks for the image.
const textRequest = "text"

// Serve is the helper: for each line on in it reads the clipboard and
// writes the image, or the text when the line asks for it, or why there
// is none, to out. It returns once in ends.
func Serve(in io.Reader, out io.Writer) error {
	return serve(in, out, read, readText)
}

func serve(in io.Reader, out io.Writer, read, readText func() ([]byte, error)) error {
	requests := bufio.NewReader(in)

	for {
		line, err := requests.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}

		reader := read
		if strings.TrimSpace(line) == textRequest {
			reader = readText
		}

		kind := answerData

		data, err := reader()
		if err != nil {
			kind, data = answerNone, []byte(err.Error())
		}

		if err := answer(out, kind, data); err != nil {
			return err
		}
	}
}

func answer(out io.Writer, kind byte, data []byte) error {
	message := binary.BigEndian.AppendUint64([]byte{kind}, uint64(len(data)))

	_, err := out.Write(append(message, data...))

	return err
}
