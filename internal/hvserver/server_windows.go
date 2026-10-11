package hvserver

import (
	"context"
	"fmt"
	"net"

	winio "github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

// hcsVM is a VM of the Host Compute Service.
type hcsVM struct{ *hcs.System }

func (v hcsVM) runtimeID(ctx context.Context) (guid.GUID, error) {
	p, err := v.Properties(ctx)

	return p.RuntimeID, err
}

// createVM makes a VM with the Host Compute Service.
func createVM(ctx context.Context, id string, doc hcs.Document) (vm, error) {
	s, err := hcs.Create(ctx, id, doc)
	if err != nil {
		return nil, err
	}

	return hcsVM{s}, nil
}

// listen listens on the pipe at path with the security sddl, the service's
// PipeSDDL but in tests. It fails if the pipe is there already: whoever
// made it would answer clients in the service's place.
func listen(path, sddl string) (net.Listener, error) {
	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}

	return l, nil
}

// pipePeer is the client of a connection on the pipe, which the pipe's
// handle tells the service about, and whose state disk disks take.
type pipePeer struct {
	pipe  windows.Handle
	disks stateDisks
	// err is why the connection is no pipe, which every question about
	// the client answers with
	err error
}

// peerOf is the peer of a connection on the pipe.
func peerOf(conn net.Conn, disks stateDisks) peer {
	pipe, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return pipePeer{err: fmt.Errorf("a connection that is no pipe: %T", conn)}
	}

	return pipePeer{pipe: windows.Handle(pipe.Fd()), disks: disks}
}

func (p pipePeer) client() (hvservice.Client, error) {
	if p.err != nil {
		return hvservice.Client{}, p.err
	}

	return hvservice.ClientOf(p.pipe)
}

func (p pipePeer) stateDisk(ctx context.Context, id string, handle uint64) (stateDisk, error) {
	if p.err != nil {
		return stateDisk{}, p.err
	}

	return p.disks.take(ctx, p.pipe, id, handle)
}
