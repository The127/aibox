package hcs

import (
	"errors"
	"fmt"
)

// hresultAccessDenied is HCS_E_ACCESS_DENIED, the result of every call by a
// process that is neither an Administrator nor a Hyper-V Administrator.
const hresultAccessDenied = 0x8037011B

// ErrAccessDenied is a call the service refused because the process is
// neither an Administrator nor a Hyper-V Administrator. An *Error with
// that HRESULT is one, by errors.Is.
var ErrAccessDenied = errors.New("the Host Compute Service serves only Administrators and Hyper-V Administrators")

// ErrClosed is a call on a System after its Close.
var ErrClosed = errors.New("the compute system is closed")

// Error is a call of the service that failed.
type Error struct {
	// Op names the call.
	Op string
	// HRESULT is the code the service failed the call with.
	HRESULT uint32
	// Result is the result document of the call, if the service wrote one:
	// JSON that says in words what the HRESULT says in a number.
	Result string
}

// Error names the call and the HRESULT, and quotes the result document if
// there is one.
func (e *Error) Error() string {
	s := fmt.Sprintf("%s: HRESULT 0x%08X", e.Op, e.HRESULT)
	if e.Result != "" {
		s += ": " + e.Result
	}

	return s
}

// Is matches ErrAccessDenied when the HRESULT is the one the service
// refuses a process without the rights with.
func (e *Error) Is(target error) bool {
	return target == ErrAccessDenied && e.HRESULT == hresultAccessDenied
}
