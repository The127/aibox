package hvservice

import (
	"errors"
	"fmt"
)

// ErrBadRequest is a request the service refuses whoever sends it.
var ErrBadRequest = errors.New("bad request")

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
	case OpStart:
		if r.Create != nil {
			return fmt.Errorf("%w: start with a VM to create", ErrBadRequest)
		}

		return nil
	}

	return fmt.Errorf("%w: unknown op %q", ErrBadRequest, r.Op)
}
