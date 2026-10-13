package hcs

import (
	"errors"
	"fmt"
)

// hresultAccessDenied is HCS_E_ACCESS_DENIED, the result of every call by a
// process that is neither an Administrator nor a Hyper-V Administrator.
const hresultAccessDenied = 0x8037011B

// hresultNoHyperV is HCS_E_HYPERV_NOT_INSTALLED, the result of a call on a
// Windows whose Host Compute Service is there, as Virtual Machine Platform
// brings it, but whose Hyper-V is off.
const hresultNoHyperV = 0x80370102

// ErrAccessDenied is the error of a call the service refused because the
// process is neither an Administrator nor a Hyper-V Administrator. An
// *Error with that HRESULT is one, by errors.Is.
var ErrAccessDenied = errors.New("the Host Compute Service serves only Administrators and Hyper-V Administrators")

// ErrNoHyperV is a Windows without the Host Compute Service, or with the
// service but with Hyper-V off, and says how to turn Hyper-V on. An *Error
// with the HRESULT of Hyper-V that is off is one, by errors.Is.
var ErrNoHyperV = errors.New("this Windows has no Hyper-V: turn it on from a PowerShell you opened as administrator, with Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V -All, and restart Windows")

// ErrClosed is the error of a call on a System after its Close.
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
// refuses a process without the rights with, and ErrNoHyperV when it is
// the one of a Windows whose Hyper-V is off.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrAccessDenied:
		return e.HRESULT == hresultAccessDenied
	case ErrNoHyperV:
		return e.HRESULT == hresultNoHyperV
	}

	return false
}
