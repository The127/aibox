package launch

import (
	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/vm"
)

// Plan is what Run of the Backend hands to Run of the package.
func (b Backend) Plan(spec backend.Spec) (vm.Machine, Options, error) {
	return b.plan(spec)
}
