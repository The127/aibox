package hvserver

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	firewallAPI      = windows.NewLazySystemDLL("FirewallAPI.dll")
	procIsolationGet = firewallAPI.NewProc("NetworkIsolationGetAppContainerConfig")
	procIsolationSet = firewallAPI.NewProc("NetworkIsolationSetAppContainerConfig")

	kernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procProcessHeap = kernel32.NewProc("GetProcessHeap")
	procHeapFree    = kernel32.NewProc("HeapFree")
)

// isolationList is the list of the containers Windows lets reach the
// loopback, through the API of the firewall, which only an administrator
// may change.
type isolationList struct{}

func (isolationList) get() ([]string, error) {
	var (
		count uint32
		list  *windows.SIDAndAttributes
	)

	if r, _, _ := procIsolationGet.Call(uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&list))); r != 0 { //nolint:gosec // pointers as the API takes them
		return nil, windows.Errno(r)
	}

	if list == nil {
		return nil, nil
	}

	entries := unsafe.Slice(list, count) //nolint:gosec // the API's array, of the count it says

	// the API's memory, which it says to free so
	defer func() {
		heap, _, _ := procProcessHeap.Call()

		for _, e := range entries {
			_, _, _ = procHeapFree.Call(heap, 0, uintptr(unsafe.Pointer(e.Sid))) //nolint:gosec // the API's memory, given back as it says
		}

		_, _, _ = procHeapFree.Call(heap, 0, uintptr(unsafe.Pointer(list))) //nolint:gosec // the API's memory, given back as it says
	}()

	sids := make([]string, 0, count)
	for _, e := range entries {
		sids = append(sids, e.Sid.String())
	}

	return sids, nil
}

func (isolationList) set(sids []string) error {
	entries := make([]windows.SIDAndAttributes, 0, len(sids))

	for _, s := range sids {
		sid, err := windows.StringToSid(s)
		if err != nil {
			return fmt.Errorf("read the SID %s: %w", s, err)
		}

		entries = append(entries, windows.SIDAndAttributes{Sid: sid})
	}

	var first *windows.SIDAndAttributes
	if len(entries) > 0 {
		first = &entries[0]
	}

	r, _, _ := procIsolationSet.Call(uintptr(len(entries)), uintptr(unsafe.Pointer(first))) //nolint:gosec // a pointer as the API takes it

	// the SIDs the entries point at, which only they hold, until the call
	// is done
	runtime.KeepAlive(entries)

	if r != 0 {
		return windows.Errno(r)
	}

	return nil
}

// registryRecord keeps the SIDs in a value of the service's own key,
// which goes with the service when it is uninstalled.
type registryRecord struct {
	key string
}

// loopbackValue is the value of the record.
const loopbackValue = "LoopbackExempt"

func (r registryRecord) load() ([]string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, r.key, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	defer func() { _ = k.Close() }()

	sids, _, err := k.GetStringsValue(loopbackValue)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}

	return sids, err
}

func (r registryRecord) save(sids []string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, r.key, registry.SET_VALUE)
	if err != nil {
		return err
	}

	defer func() { _ = k.Close() }()

	if len(sids) == 0 {
		if err := k.DeleteValue(loopbackValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}

		return nil
	}

	return k.SetStringsValue(loopbackValue, sids)
}

// newExemptions are the exemptions on the list of the machine, recorded in
// the key of the service.
func newExemptions() *exemptions {
	return &exemptions{list: isolationList{}, record: registryRecord{key: `SYSTEM\CurrentControlSet\Services\` + ServiceName + `\Parameters`}}
}

// SweepLoopback takes the containers of VMs of the service off the list,
// as the service does when it starts. An uninstall does it, as it removes
// the record of them with the service.
func SweepLoopback() error {
	return newExemptions().sweep()
}
