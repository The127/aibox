package hvlaunch

import (
	"io"
)

// consoleSource is the pipe the console of the VM comes over, as
// hvservice.Console is, which the tests fake.
type consoleSource interface {
	Listen() error
	Connect() (io.ReadCloser, error)
	Close() error
}

// serveConsole copies the console of the VM, which Hyper-V connects to
// the pipe once as the VM starts, into the log until Hyper-V lets go of
// it, which it does when the VM is gone, and closes gone then. The log
// must take everything, as host.ConsoleLog does, or the copy would end
// before the VM. The console is the pipe's to close.
func serveConsole(console consoleSource, log io.Writer) (gone <-chan struct{}) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		conn, err := console.Connect()
		if err != nil {
			return
		}

		_, _ = io.Copy(log, conn)
	}()

	return done
}
