package hvserver

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hvservice"
)

var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// memoryStatusEx is MEMORYSTATUSEX, which x/sys/windows lacks.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// host is what the machine has, read anew for each VM, since memory can
// be added to a running machine, and processors too.
func host() (hvservice.Host, error) {
	status := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}

	if r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status))); r == 0 { //nolint:gosec // the struct is handed to the call as the ABI asks
		return hvservice.Host{}, fmt.Errorf("read the memory of the host: %w", err)
	}

	return hvservice.Host{
		MemoryMiB: int(status.totalPhys >> 20),
		CPUs:      int(windows.GetActiveProcessorCount(windows.ALL_PROCESSOR_GROUPS)),
	}, nil
}
