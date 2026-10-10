//go:build !windows

package session

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize reports on the returned channel each time the terminal
// changes size, which the kernel says with SIGWINCH, until stop is closed.
func notifyResize(_ int, stop <-chan struct{}) <-chan struct{} {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)

	changed := make(chan struct{}, 1)

	go func() {
		defer signal.Stop(winch)

		for {
			select {
			case <-winch:
				select {
				case changed <- struct{}{}:
				default:
				}
			case <-stop:
				return
			}
		}
	}()

	return changed
}
