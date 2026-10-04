package session

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// PTY is a pseudo terminal. Master is the side a session reads and writes,
// Slave is the terminal of the command.
type PTY struct {
	Master *os.File
	Slave  *os.File
}

// OpenPTY opens a pseudo terminal of the size.
func OpenPTY(size Size) (*PTY, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}

	fd := int(master.Fd())

	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()

		return nil, fmt.Errorf("unlock the terminal: %w", err)
	}

	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()

		return nil, fmt.Errorf("number of the terminal: %w", err)
	}

	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()

		return nil, err
	}

	pty := &PTY{Master: master, Slave: slave}
	if err := pty.Resize(size); err != nil {
		_ = pty.Close()

		return nil, err
	}

	return pty, nil
}

// Resize sets the size of the terminal.
func (p *PTY) Resize(size Size) error {
	return unix.IoctlSetWinsize(int(p.Master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: size.Rows, Col: size.Cols})
}

// Close closes both sides.
func (p *PTY) Close() error {
	slaveErr := p.Slave.Close()
	if err := p.Master.Close(); err != nil {
		return err
	}

	return slaveErr
}
