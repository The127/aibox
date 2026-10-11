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

// ClientOf tells who the client on the other end of the pipe is.
func ClientOf(pipe windows.Handle) (Client, error) {
	var client Client

	err := AsClient(pipe, func(token windows.Token) error {
		var err error
		client, err = clientOf(token)

		return err
	})

	return client, err
}

// AsClient runs fn with the token of the client on the other end of the
// pipe, on a thread that acts as the client meanwhile, which is how
// Windows hands the token out. The pipe must have carried a message from
// the client already, since Windows tells the service only then. The
// client needs to let the service identify it, and no more: the token
// tells who the client is and what they may do, but can't act as them.
func AsClient(pipe windows.Handle, fn func(windows.Token) error) error {
	done := make(chan error, 1)

	// The thread acts as the client in a goroutine of its own. If it could
	// not stop, it stays locked, and Go ends the thread with the goroutine
	// instead of running anything else on it.
	go func() {
		runtime.LockOSThread()

		if r, _, err := procImpersonateNamedPipeClient.Call(uintptr(pipe)); r == 0 {
			runtime.UnlockOSThread()

			done <- fmt.Errorf("identify the client: %w", err)

			return
		}

		err := withThreadToken(fn)

		if revertErr := windows.RevertToSelf(); revertErr != nil {
			done <- fmt.Errorf("stop acting as the client: %w", revertErr)

			return
		}

		runtime.UnlockOSThread()

		done <- err
	}()

	return <-done
}

// withThreadToken runs fn with the token the thread acts under.
func withThreadToken(fn func(windows.Token) error) error {
	var token windows.Token

	// as self: the service reads the token with its own rights, which the
	// client's need not include
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token)
	if errors.Is(err, windows.ERROR_CANT_OPEN_ANONYMOUS) {
		return ErrAnonymous
	}

	if err != nil {
		return fmt.Errorf("read the token of the client: %w", err)
	}

	defer func() { _ = token.Close() }()

	return fn(token)
}

// clientOf reads who the token is.
func clientOf(token windows.Token) (Client, error) {
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
