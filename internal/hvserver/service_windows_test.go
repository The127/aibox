package hvserver

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/windows/svc"
)

// execute runs h as the service manager would, and returns the states it
// told, and what Execute returned.
func execute(h *handler, requests chan svc.ChangeRequest) (states chan svc.State, returned chan [2]any) {
	status := make(chan svc.Status)
	states = make(chan svc.State, 8)
	returned = make(chan [2]any, 1)

	go func() {
		for s := range status {
			states <- s.State
		}
	}()

	go func() {
		ssec, code := h.Execute(nil, requests, status)
		returned <- [2]any{ssec, code}
	}()

	return states, returned
}

func TestTheServiceRunsOnceReadyAndStopsWhenAsked(t *testing.T) {
	t.Parallel()

	// arrange
	stopped := make(chan struct{})
	h := &handler{
		serve: func(ctx context.Context, ready func()) error {
			ready()
			<-ctx.Done()
			close(stopped)

			return nil
		},
		fail: func(err error) { t.Errorf("the service failed: %v", err) },
	}
	requests := make(chan svc.ChangeRequest)
	states, returned := execute(h, requests)

	assert.Equal(t, svc.StartPending, <-states)
	assert.Equal(t, svc.Running, <-states)

	// act
	requests <- svc.ChangeRequest{Cmd: svc.Stop}

	// assert
	assert.Equal(t, svc.StopPending, <-states)
	assert.Equal(t, [2]any{false, uint32(0)}, <-returned)
	<-stopped
}

func TestTheServiceFailsIfItEndsUnasked(t *testing.T) {
	t.Parallel()

	for name, readyFirst := range map[string]bool{"before it is ready": false, "once it runs": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			var failed error
			h := &handler{
				serve: func(_ context.Context, ready func()) error {
					if readyFirst {
						ready()
					}

					return errors.New("the pipe is someone else's")
				},
				fail: func(err error) { failed = err },
			}

			// act
			_, returned := execute(h, make(chan svc.ChangeRequest))

			// assert
			assert.Equal(t, [2]any{true, uint32(1)}, <-returned)
			assert.ErrorContains(t, failed, "the pipe is someone else's")
		})
	}
}
