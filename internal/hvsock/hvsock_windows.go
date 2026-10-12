package hvsock

import (
	"fmt"
	"net"
	"sync"

	winio "github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
)

// Listen listens on the port of the VM, until the listener is closed.
func Listen(vm guid.GUID, port uint32) (net.Listener, error) {
	l, err := winio.ListenHvsock(&winio.HvsockAddr{VMID: vm, ServiceID: winio.VsockServiceID(port)})
	if err != nil {
		return nil, fmt.Errorf("listen on port %d of the VM: %w", port, err)
	}

	return Closing(l), nil
}

// Closing is the listener, whose Accept fails with net.ErrClosed once it is
// closed, which the proxy and the terminal take as the end of the VM,
// where winio's own error for that would be reported as a failure.
func Closing(l net.Listener) net.Listener {
	return &closing{Listener: l, closed: make(chan struct{})}
}

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
