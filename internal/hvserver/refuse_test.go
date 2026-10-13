package hvserver

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

func TestRefusalTellsAibox(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err  error
		want string
	}{
		"a request it got wrong, as it is": {
			fmt.Errorf("%w: 9 processors, not between 1 and the 8 the host has", hvservice.ErrBadRequest),
			"bad request: 9 processors, not between 1 and the 8 the host has",
		},
		"that the host has too little memory left, as it is": {
			fmt.Errorf("%w: 8000 of 16000 MiB in use, too little for 9000 MiB", ErrNoMemory),
			"the host has too little memory left: 8000 of 16000 MiB in use, too little for 9000 MiB",
		},
		"of the Host Compute Service only its HRESULT": {
			fmt.Errorf("create the VM: %w", &hcs.Error{Op: "HcsCreateComputeSystem", HRESULT: 0x80070002, Result: `{"ErrorMessage":"C:\\ProgramData\\aibox\\vm\\os.vhd"}`}),
			"the service could not start the VM: HRESULT 0x80070002 (more in the Application event log, source aibox)",
		},
		"that Hyper-V is off, and how to turn it on": {
			fmt.Errorf("create the VM: %w", &hcs.Error{Op: "HcsCreateComputeSystem", HRESULT: 0x80370102, Result: `{"ErrorMessage":"C:\\ProgramData\\aibox\\vm"}`}),
			hcs.ErrNoHyperV.Error(),
		},
		"nothing of any other error": {
			errors.New(`open C:\ProgramData\aibox\vm\vmlinuz-hyperv: access denied`),
			"the service could not start the VM (more in the Application event log, source aibox)",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			reply := refusal("start", tt.err)

			// assert
			assert.Equal(t, tt.want, reply.Error)
		})
	}
}
