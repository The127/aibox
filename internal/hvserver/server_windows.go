package hvserver

import (
	"context"
	"errors"
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
// handle tells the service about.
type pipePeer struct {
	pipe windows.Handle
	// err is why the connection is no pipe, which every question about
	// the client answers with
	err error
}

// peerOf is the peer of a connection on the pipe.
func peerOf(conn net.Conn) peer {
	pipe, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return pipePeer{err: fmt.Errorf("a connection that is no pipe: %T", conn)}
	}

	return pipePeer{pipe: windows.Handle(pipe.Fd())}
}

func (p pipePeer) client() (hvservice.Client, error) {
	if p.err != nil {
		return hvservice.Client{}, p.err
	}

	return hvservice.ClientOf(p.pipe)
}

func (p pipePeer) stateDisk(context.Context, string, uint64) (stateDisk, error) {
	if p.err != nil {
		return stateDisk{}, p.err
	}

	return stateDisk{}, errStateByHandle
}

// errStateByHandle is a state disk by handle, which the service can't
// take yet.
var errStateByHandle = errors.New("the state disk by handle is not taken yet")
