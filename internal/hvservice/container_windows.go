package hvservice

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procDeriveSID = windows.NewLazySystemDLL("userenv.dll").NewProc("DeriveAppContainerSidFromAppContainerName")

// ContainerSID is the SID of the AppContainer of the name, which depends on
// the name alone.
func ContainerSID(name string) (string, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}

	var sid *windows.SID

	if r, _, _ := procDeriveSID.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(&sid))); r != 0 { //nolint:gosec // pointers as the API takes them
		return "", fmt.Errorf("derive the SID of the container %s: HRESULT 0x%08X", name, uint32(r)) //nolint:gosec // an HRESULT is 32 bits
	}

	defer windows.FreeSid(sid) //nolint:errcheck // after the string is made of it

	return sid.String(), nil
}
