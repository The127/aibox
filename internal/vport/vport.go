// Package vport carries the connections of the VM to aibox over one virtio
// serial port, for a Linux host whose vsock cannot be kept private. A port
// is one lasting stream, so the connections are streams multiplexed on it.
// Each stream starts with the number of the service it is for, as a
// connection over vsock goes to a port.
package vport

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// config keeps yamux from logging, since its log would reach the terminal
// of the person, and in the VM the console. It keeps no watch on the other
// side and lets a write wait as long as it takes, like one over vsock: the
// port closing when QEMU exits ends the session. A VM that hangs but runs
// on is not noticed, as over vsock.
func config() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.EnableKeepAlive = false
	c.ConnectionWriteTimeout = time.Duration(math.MaxInt64)

	return c
}

// Find is the device, under dev, of the virtio port with the name, as the
// sysfs mounted at sys tells. It wraps fs.ErrNotExist when the VM has no
// such port.
func Find(sys, dev, name string) (string, error) {
	ports := filepath.Join(sys, "class", "virtio-ports")

	devices, err := os.ReadDir(ports)
	if err != nil {
		return "", err
	}

	for _, device := range devices {
		// a port without a name may have no name to read
		named, err := os.ReadFile(filepath.Join(ports, device.Name(), "name")) //nolint:gosec // a file of the sysfs the caller names
		if err == nil && strings.TrimSpace(string(named)) == name {
			return filepath.Join(dev, device.Name()), nil
		}
	}

	return "", fmt.Errorf("no virtio port named %q: %w", name, fs.ErrNotExist)
}

// Dialer is the side of the VM.
type Dialer struct {
	session *yamux.Session
}

// NewDialer opens the side of the VM on the port.
func NewDialer(port io.ReadWriteCloser) (*Dialer, error) {
	session, err := yamux.Client(port, config())
	if err != nil {
		return nil, err
	}

	return &Dialer{session: session}, nil
}

// Dial connects to the service of the host.
func (d *Dialer) Dial(service uint32) (net.Conn, error) {
	stream, err := d.session.OpenStream()
	if err != nil {
		return nil, err
	}

	if err := binary.Write(stream, binary.BigEndian, service); err != nil {
		_ = stream.Close()

		return nil, err
	}

	return stream, nil
}

// Listen opens the side of the host on the port and returns a listener for
// each service, in their order. The port closes when every listener is
// closed.
func Listen(port io.ReadWriteCloser, services ...uint32) ([]net.Listener, error) {
	session, err := yamux.Server(port, config())
	if err != nil {
		return nil, err
	}

	m := &mux{session: session, open: len(services), byService: map[uint32]*listener{}}
	listeners := make([]net.Listener, len(services))

	for i, service := range services {
		l := &listener{mux: m, service: service, conns: make(chan net.Conn), closed: make(chan struct{})}
		m.byService[service] = l
		listeners[i] = l
	}

	go m.route()

	return listeners, nil
}

// mux hands each stream of the session to the listener of its service.
type mux struct {
	session   *yamux.Session
	byService map[uint32]*listener

	mu   sync.Mutex
	open int
}

func (m *mux) route() {
	for {
		stream, err := m.session.AcceptStream()
		if err != nil {
			return
		}

		// the VM may be slow to say what it wants, or never say it, which
		// must not hold up its other streams
		go m.hand(stream)
	}
}

// headerPatience is how long a stream may take to name its service. The
// VM names it as it opens the stream, and a stream that does not would
// hold its memory in aibox until the VM ends.
const headerPatience = 10 * time.Second

func (m *mux) hand(stream *yamux.Stream) {
	var service uint32

	_ = stream.SetReadDeadline(time.Now().Add(headerPatience))

	if err := binary.Read(stream, binary.BigEndian, &service); err != nil {
		_ = stream.Close()

		return
	}

	_ = stream.SetReadDeadline(time.Time{})

	l, ok := m.byService[service]
	if !ok {
		_ = stream.Close()

		return
	}

	select {
	case l.conns <- stream:
	case <-l.closed:
		_ = stream.Close()
	case <-m.session.CloseChan():
		_ = stream.Close()
	}
}

// closeOne closes the session once the last listener is closed.
func (m *mux) closeOne() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.open--
	if m.open == 0 {
		_ = m.session.Close()
	}
}

type listener struct {
	mux     *mux
	service uint32
	conns   chan net.Conn
	closed  chan struct{}
	once    sync.Once
}

func (l *listener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	case <-l.mux.session.CloseChan():
		return nil, net.ErrClosed
	}
}

func (l *listener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		l.mux.closeOne()
	})

	return nil
}

func (l *listener) Addr() net.Addr {
	return addr(l.service)
}

// addr is the service a listener takes the streams of.
type addr uint32

func (addr) Network() string {
	return "vport"
}

func (a addr) String() string {
	return strconv.FormatUint(uint64(a), 10)
}
