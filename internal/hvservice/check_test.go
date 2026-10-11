package hvservice_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/hvservice"
)

func TestCheckTakesACreateWithItsVMAndABareStart(t *testing.T) {
	t.Parallel()

	for _, request := range []hvservice.Request{
		{Op: hvservice.OpCreate, Create: &hvservice.Create{}},
		{Op: hvservice.OpStart},
	} {
		t.Run(string(request.Op), func(t *testing.T) {
			t.Parallel()

			// act
			err := request.Check()

			// assert
			assert.NoError(t, err)
		})
	}
}

func TestCheckRefusesARequestTheServiceWouldNotDoAsMeant(t *testing.T) {
	t.Parallel()

	tests := map[string]hvservice.Request{
		"no op":                 {},
		"an unknown op":         {Op: "delete"},
		"create without its VM": {Op: hvservice.OpCreate},
		"start with a VM":       {Op: hvservice.OpStart, Create: &hvservice.Create{}},
	}

	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			err := request.Check()

			// assert
			assert.ErrorIs(t, err, hvservice.ErrBadRequest)
		})
	}
}

var host = hvservice.Host{MemoryMiB: 16384, CPUs: 8}

// vm is a VM the host can take, changed by change.
func vm(change func(*hvservice.Create)) hvservice.Create {
	c := hvservice.Create{MemoryMiB: 2048, CPUs: 2, Cmdline: "console=ttyS0 aibox.hyperv"}
	change(&c)

	return c
}

func TestCheckTakesAVMTheHostAndTheKernelCanTake(t *testing.T) {
	t.Parallel()

	tests := map[string]hvservice.Create{
		"a VM":           vm(func(*hvservice.Create) {}),
		"all the memory": vm(func(c *hvservice.Create) { c.MemoryMiB = host.MemoryMiB }),
		"all the CPUs":   vm(func(c *hvservice.Create) { c.CPUs = host.CPUs }),
		"the longest kernel command line": vm(func(c *hvservice.Create) {
			c.Cmdline = strings.Repeat("x", hvservice.MaxCmdline)
		}),
	}

	for name, c := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			err := c.Check(host)

			// assert
			assert.NoError(t, err)
		})
	}
}

func TestCheckRefusesAVMTheHostOrTheKernelCantTake(t *testing.T) {
	t.Parallel()

	tests := map[string]hvservice.Create{
		"no memory":                     vm(func(c *hvservice.Create) { c.MemoryMiB = 0 }),
		"less than no memory":           vm(func(c *hvservice.Create) { c.MemoryMiB = -1 }),
		"more memory than the host has": vm(func(c *hvservice.Create) { c.MemoryMiB = host.MemoryMiB + 1 }),
		"no CPU":                        vm(func(c *hvservice.Create) { c.CPUs = 0 }),
		"more CPUs than the host has":   vm(func(c *hvservice.Create) { c.CPUs = host.CPUs + 1 }),
		"a kernel command line the kernel cuts": vm(func(c *hvservice.Create) {
			c.Cmdline = strings.Repeat("x", hvservice.MaxCmdline+1)
		}),
		"a kernel command line with a NUL": vm(func(c *hvservice.Create) { c.Cmdline = "console=ttyS0\x00aibox.hyperv" }),
	}

	for name, c := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			err := c.Check(host)

			// assert
			assert.ErrorIs(t, err, hvservice.ErrBadRequest)
		})
	}
}
