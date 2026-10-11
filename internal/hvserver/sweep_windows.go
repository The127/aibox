package hvserver

import (
	"context"
	"errors"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

// leftBehind lists the ids of the service's VMs, by the owner their
// Document names.
func leftBehind(ctx context.Context) ([]string, error) {
	systems, err := hcs.Enumerate(ctx, hvservice.Owner)
	if err != nil {
		return nil, err
	}

	ids := make([]string, len(systems))
	for i, s := range systems {
		ids[i] = s.ID
	}

	return ids, nil
}

// endVM ends the VM with the id. A VM ends with the last handle to it,
// which went with the service that made it; one still listed outlived
// that, so it is ended outright rather than by a handle again.
func endVM(ctx context.Context, id string) error {
	s, err := hcs.Open(ctx, id)
	if err != nil {
		return err
	}

	return errors.Join(s.Terminate(ctx), s.Close())
}
