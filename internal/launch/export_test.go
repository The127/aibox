//go:build linux

package launch

import (
	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/vm"
)

// Prepare readies the host for the spec and returns what Run of the Backend
// hands to Run of the package.
func (b Backend) Prepare(spec backend.Spec) (vm.Machine, Options, error) {
	return b.prepare(spec)
}
