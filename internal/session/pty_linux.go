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

	var number int

	err = control(master, func(fd int) error {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
			return fmt.Errorf("unlock the terminal: %w", err)
		}

		number, err = unix.IoctlGetInt(fd, unix.TIOCGPTN)
		if err != nil {
			return fmt.Errorf("number of the terminal: %w", err)
		}

		return nil
	})
	if err != nil {
		_ = master.Close()

		return nil, err
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
	return control(p.Master, func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: size.Rows, Col: size.Cols})
	})
}

// Close closes both sides.
func (p *PTY) Close() error {
	slaveErr := p.Slave.Close()
	if err := p.Master.Close(); err != nil {
		return err
	}

	return slaveErr
}

// control runs the ioctl on the file without taking the file out of the Go
// runtime's poller, which File.Fd would do.
func control(file *os.File, ioctl func(fd int) error) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}

	var ioctlErr error

	if err := raw.Control(func(fd uintptr) { ioctlErr = ioctl(int(fd)) }); err != nil {
		return err
	}

	return ioctlErr
}
