package session

import (
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

const fallbackTerm = "xterm-256color"

var defaultSize = Size{Rows: 24, Cols: 80}

// Open prepares the terminal on stdin for a session: raw mode, its size and
// size changes. When stdin is not a terminal the client gets a default size
// and no size changes. The returned function puts the terminal back.
func Open(stdin *os.File, out io.Writer) (Client, func(), error) {
	client := Client{In: stdin, Out: out, Term: os.Getenv("TERM"), Size: defaultSize}
	if client.Term == "" {
		client.Term = fallbackTerm
	}

	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		return client, func() {}, nil
	}

	size, err := sizeOfTerminal(fd)
	if err != nil {
		return Client{}, nil, err
	}

	state, err := term.MakeRaw(fd)
	if err != nil {
		return Client{}, nil, err
	}

	client.Size = size

	resized := make(chan Size, 1)
	client.Resized = resized

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)

	stop := make(chan struct{})

	go func() {
		for {
			select {
			case <-winch:
				if size, err := sizeOfTerminal(fd); err == nil {
					// a size that was not picked up yet is replaced
					select {
					case <-resized:
					default:
					}

					resized <- size
				}
			case <-stop:
				return
			}
		}
	}()

	restore := func() {
		signal.Stop(winch)
		close(stop)
		_ = term.Restore(fd, state)
	}

	return client, restore, nil
}

func sizeOfTerminal(fd int) (Size, error) {
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return Size{}, err
	}

	return Size{Rows: uint16(rows), Cols: uint16(cols)}, nil //nolint:gosec // the kernel reports them as 16 bit values
}
