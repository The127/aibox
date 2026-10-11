package hvlaunch

import (
	"fmt"
	"net"
	"sync"

	winio "github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"

	"github.com/the127/aibox/internal/hvservice"
)

// listenVM listens on a Hyper-V socket port of the VM, which the guest
// reaches as a vsock port of the host.
func listenVM(vm guid.GUID, port uint32) (net.Listener, error) {
	l, err := winio.ListenHvsock(&winio.HvsockAddr{VMID: vm, ServiceID: winio.VsockServiceID(port)})
	if err != nil {
		return nil, fmt.Errorf("listen on port %d of the VM: %w", port, err)
	}

	return &closing{Listener: l, closed: make(chan struct{})}, nil
}

// consoleOf is the console pipe under the handle the service put into
// this process.
func consoleOf(handle uint64) consoleSource {
	return hvservice.ConsoleOf(handle)
}

// closing is a listener whose Accept fails with net.ErrClosed once it is
// closed, which the proxy and the terminal take as the end of the VM,
// where winio's own error for that would be reported as a failure.
type closing struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *closing) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		select {
		case <-l.closed:
			return nil, net.ErrClosed
		default:
			return nil, err
		}
	}

	return conn, nil
}

func (l *closing) Close() error {
	l.once.Do(func() { close(l.closed) })

	return l.Listener.Close()
}
