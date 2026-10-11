package hvservice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// ErrNotService is a pipe of the name that the service didn't make.
var ErrNotService = errors.New("the pipe is not the service's")

// pipeUse is what a client may do with the pipe: read and write messages,
// but not add an instance of its own, which FILE_APPEND_DATA would let it,
// and which would answer the next client in the service's place.
const pipeUse = (windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE) &^ windows.FILE_APPEND_DATA

// PipeSDDL is the security of the service's pipe: SYSTEM owns it, as Dial
// checks, and the people who sit at the machine and their AppContainers
// may use it. A logon over the network, such as SSH, is not one of them:
// such a person could not reach the VM's console or terminal either. The
// low mandatory label lets an AppContainer, which runs at low integrity,
// write to it.
var PipeSDDL = pipeSDDL("SY", "IU", "AC")

// pipeSDDL is PipeSDDL with the owner and the users, so that a test, which
// can't make SYSTEM the owner, can make a pipe like it of its own.
func pipeSDDL(owner string, users ...string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "O:%sD:P(A;;GA;;;SY)", owner)

	for _, user := range users {
		fmt.Fprintf(&b, "(A;;0x%X;;;%s)", pipeUse, user)
	}

	b.WriteString("S:(ML;;NW;;;LW)")

	return b.String()
}

// Dial connects to the service. The service learns who the caller is and
// nothing more: it can't act as the caller. Dial refuses a pipe that
// SYSTEM doesn't own, which only the service can have made, since making
// SYSTEM the owner takes a privilege a person at the machine doesn't have.
func Dial(ctx context.Context) (net.Conn, error) {
	return dial(ctx, PipeName, func(owner *windows.SID) bool {
		return owner.IsWellKnown(windows.WinLocalSystemSid)
	})
}

// dial is Dial to the pipe at path, which it takes if owned takes its
// owner.
func dial(ctx context.Context, path string, owned func(*windows.SID) bool) (net.Conn, error) {
	conn, err := winio.DialPipeAccessImpLevel(ctx, path, pipeUse, winio.PipeImpLevelIdentification)
	if err != nil {
		return nil, fmt.Errorf("connect to the aibox service: %w", err)
	}

	h := windows.Handle(conn.(interface{ Fd() uintptr }).Fd())

	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("read who owns the pipe of the aibox service: %w", err)
	}

	owner, _, err := sd.Owner()
	if err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("read who owns the pipe of the aibox service: %w", err)
	}

	if !owned(owner) {
		_ = conn.Close()

		return nil, fmt.Errorf("%w: %s owns it", ErrNotService, owner)
	}

	return conn, nil
}
