package hvservice

import (
	"errors"
	"fmt"
	"strings"
)

// ErrBadRequest is a request the service refuses whoever sends it.
var ErrBadRequest = errors.New("bad request")

// Host is what the machine has, which no VM can be given more of.
type Host struct {
	// MemoryMiB is the physical memory of the machine, in MiB.
	MemoryMiB int
	// CPUs is the number of logical processors of the machine.
	CPUs int
}

// Check refuses a request the service would not do as it was meant: one
// without an op or with one the service doesn't know, a create without
// the VM to create, and a start that names a VM.
func (r Request) Check() error {
	switch r.Op {
	case OpCreate:
		if r.Create == nil {
			return fmt.Errorf("%w: create without the VM to create", ErrBadRequest)
		}

		return nil
	case OpStart, OpStop:
		if r.Create != nil {
			return fmt.Errorf("%w: %s with a VM to create", ErrBadRequest, r.Op)
		}

		return nil
	}

	return fmt.Errorf("%w: unknown op %q", ErrBadRequest, r.Op)
}

// Check refuses a VM the host or the kernel can't take as asked.
func (c Create) Check(host Host) error {
	// HCS takes a VM larger than the host's memory and only fails or
	// thrashes the host once it starts
	if c.MemoryMiB < 1 || c.MemoryMiB > host.MemoryMiB {
		return fmt.Errorf("%w: memory of %d MiB, not between 1 and the %d the host has", ErrBadRequest, c.MemoryMiB, host.MemoryMiB)
	}

	if c.CPUs < 1 || c.CPUs > host.CPUs {
		return fmt.Errorf("%w: %d processors, not between 1 and the %d the host has", ErrBadRequest, c.CPUs, host.CPUs)
	}

	if len(c.Cmdline) > MaxCmdline {
		return fmt.Errorf("%w: a kernel command line of %d bytes, which the kernel cuts after %d", ErrBadRequest, len(c.Cmdline), MaxCmdline)
	}

	// the kernel takes the command line as a C string
	if strings.ContainsRune(c.Cmdline, 0) {
		return fmt.Errorf("%w: a kernel command line with a NUL, where the kernel would end it", ErrBadRequest)
	}

	// the guest doesn't run without its state disk, and no handle is 0
	if c.State == 0 {
		return fmt.Errorf("%w: no state disk", ErrBadRequest)
	}

	if c.Network != nil {
		if err := checkPorts("port", c.Network.Ports); err != nil {
			return err
		}

		if err := checkPorts("loopback port", c.Network.Loopback); err != nil {
			return err
		}
	}

	return nil
}

// checkPorts refuses port 0, which is no port to connect to, and a port
// named twice, which also keeps a list no longer than there are ports.
func checkPorts(what string, ports []uint16) error {
	seen := make(map[uint16]bool, len(ports))

	for _, p := range ports {
		if p == 0 {
			return fmt.Errorf("%w: %s 0", ErrBadRequest, what)
		}

		if seen[p] {
			return fmt.Errorf("%w: %s %d twice", ErrBadRequest, what, p)
		}

		seen[p] = true
	}

	return nil
}
