package hvlaunch

import (
	"fmt"
	"net"
	"os/user"
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

// listenConsole listens on the pipe the service connects the VM's console
// to, which only this user and Hyper-V may open.
func listenConsole(path string) (net.Listener, error) {
	me, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("find who runs aibox: %w", err)
	}

	sddl, err := hvservice.ConsoleSDDL(me.Uid)
	if err != nil {
		return nil, err
	}

	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		return nil, fmt.Errorf("listen for the console: %w", err)
	}

	return l, nil
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
