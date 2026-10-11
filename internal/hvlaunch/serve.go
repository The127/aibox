package hvlaunch

import (
	"io"
	"net"

	"github.com/hugelgupf/p9/p9"
)

// serveConsole copies the console of the VM, which Hyper-V connects to the
// listener once as the VM starts, into the log until Hyper-V lets go of
// it, which it does when the VM is gone, and closes gone then. The log
// must take everything, as host.ConsoleLog does, or the copy would end
// before the VM. Hyper-V is the only one let at the listener besides this
// user, so the first connection is the console.
func serveConsole(l net.Listener, log io.Writer) (gone <-chan struct{}) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		conn, err := l.Accept()
		if err != nil {
			return
		}

		_, _ = io.Copy(log, conn)
		_ = conn.Close()
	}()

	return done
}

// serveShares serves the folders of the host to each connection the guest
// makes to the listener, as the guest mounts each share over a connection
// of its own, until the listener closes.
func serveShares(l net.Listener, fs p9.Attacher) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}

		go func() { _ = p9.NewServer(fs).Handle(conn, conn) }()
	}
}
