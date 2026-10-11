package hvservice

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procImpersonateNamedPipeClient = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

// tokenAppContainerSid is TokenAppContainerSid, which x/sys/windows lacks.
const tokenAppContainerSid = 31

// ErrAnonymous is a client that doesn't let the service know who it is,
// or is no one: the sockets of its VM could open to no user.
var ErrAnonymous = errors.New("the client is anonymous")

// ClientOf tells who the client on the other end of the pipe is. The
// pipe must have carried a message from the client already, since Windows
// tells the service only then. The client needs to let the service
// identify it, and no more: the service only reads who it is.
func ClientOf(pipe windows.Handle) (Client, error) {
	type result struct {
		client Client
		err    error
	}

	done := make(chan result, 1)

	// The thread acts as the client in a goroutine of its own. If it could
	// not stop, it stays locked, and Go ends the thread with the goroutine
	// instead of running anything else on it.
	go func() {
		runtime.LockOSThread()

		if r, _, err := procImpersonateNamedPipeClient.Call(uintptr(pipe)); r == 0 {
			runtime.UnlockOSThread()

			done <- result{err: fmt.Errorf("identify the client: %w", err)}

			return
		}

		var res result
		res.client, res.err = clientOfThread()

		if err := windows.RevertToSelf(); err != nil {
			done <- result{err: fmt.Errorf("stop acting as the client: %w", err)}

			return
		}

		runtime.UnlockOSThread()

		done <- res
	}()

	res := <-done

	return res.client, res.err
}

// clientOfThread reads who the thread acts as.
func clientOfThread() (Client, error) {
	var token windows.Token

	// as self: the service reads the token with its own rights, which the
	// client's need not include
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token)
	if errors.Is(err, windows.ERROR_CANT_OPEN_ANONYMOUS) {
		return Client{}, ErrAnonymous
	}

	if err != nil {
		return Client{}, fmt.Errorf("read the token of the client: %w", err)
	}

	defer func() { _ = token.Close() }()

	user, err := token.GetTokenUser()
	if err != nil {
		return Client{}, fmt.Errorf("read the user of the client: %w", err)
	}

	if user.User.Sid.IsWellKnown(windows.WinAnonymousSid) {
		return Client{}, ErrAnonymous
	}

	appContainer, err := appContainerSID(token)
	if err != nil {
		return Client{}, err
	}

	return Client{User: user.User.Sid.String(), AppContainer: appContainer}, nil
}

// appContainerSID is the SID of the AppContainer of the token, or empty.
func appContainerSID(token windows.Token) (string, error) {
	var n uint32

	err := windows.GetTokenInformation(token, tokenAppContainerSid, nil, 0, &n)
	if err != nil && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return "", fmt.Errorf("read the AppContainer of the client: %w", err)
	}

	if n < uint32(unsafe.Sizeof(uintptr(0))) {
		return "", fmt.Errorf("read the AppContainer of the client: %d bytes", n)
	}

	buf := make([]byte, n)
	if err := windows.GetTokenInformation(token, tokenAppContainerSid, &buf[0], n, &n); err != nil {
		return "", fmt.Errorf("read the AppContainer of the client: %w", err)
	}

	// TOKEN_APPCONTAINER_INFORMATION holds one pointer, nil outside an
	// AppContainer
	sid := *(**windows.SID)(unsafe.Pointer(&buf[0])) //nolint:gosec // n holds the pointer, as checked above
	if sid == nil {
		return "", nil
	}

	return sid.String(), nil
}
