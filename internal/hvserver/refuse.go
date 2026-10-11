package hvserver

import (
	"errors"
	"fmt"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

// eventLog is where the service writes the whole of an error it tells
// aibox only part of.
const eventLog = "the Application event log, source aibox"

// refusal is what aibox is told of why the service could not do what it
// asked, what being the verb of it. A request it got wrong, or one the
// host has too little left for, is told as it is. Of anything else aibox
// learns no more than the HRESULT of the Host Compute Service: its result
// document, and the errors of the service, may name the paths of the
// installation and say what SYSTEM sees of the host, which are no
// business of whoever runs aibox. The caller logs err whole.
func refusal(what string, err error) hvservice.Reply {
	if errors.Is(err, hvservice.ErrBadRequest) || errors.Is(err, ErrNoMemory) {
		return hvservice.Refusal(err)
	}

	var hcsErr *hcs.Error
	if errors.As(err, &hcsErr) {
		return hvservice.Refusal(fmt.Errorf("the service could not %s the VM: HRESULT 0x%08X (more in %s)", what, hcsErr.HRESULT, eventLog))
	}

	return hvservice.Refusal(fmt.Errorf("the service could not %s the VM (more in %s)", what, eventLog))
}
