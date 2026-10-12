package hvlaunch

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/appcontainer"
	"github.com/the127/aibox/internal/hvservice"
)

// claimPrefix puts the claims in the global namespace, so that a run of
// the user in another session sees the claims of this one.
const claimPrefix = `Global\`

// claim holds the name of the container until the handle is closed,
// which Windows does when aibox ends, however it ends: a profile whose
// name no one holds is one a run left behind.
func claim(name string) (windows.Handle, error) {
	n, err := windows.UTF16PtrFromString(claimPrefix + strings.ToLower(name))
	if err != nil {
		return 0, err
	}

	h, err := windows.CreateMutex(nil, false, n)
	// the name the service picked anew for this run, held already by
	// someone who learned it, which only keeps the profile from a sweep
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return 0, fmt.Errorf("hold the container %s: %w", name, err)
	}

	return h, nil
}

// claimed is whether a run holds the name of the container. A name that
// can't be checked is held, so that no sweep takes the profile of a run.
func claimed(name string) bool {
	n, err := windows.UTF16PtrFromString(claimPrefix + strings.ToLower(name))
	if err != nil {
		return true
	}

	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, n)
	if err == nil {
		_ = windows.CloseHandle(h)

		return true
	}

	return !errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
}

// sweepProfiles removes the profiles of the containers of aibox no run
// holds: those of runs that ended before they could remove them, as one
// killed does. A run claims its container before it makes the profile,
// so a run that starts meanwhile keeps its own. While another run sweeps
// it does nothing: that sweep removes the same profiles, and one it
// misses is the next run's.
func sweepProfiles() error {
	unlock, err := trySweep()
	// another sweep holds the lock
	if err != nil || unlock == nil {
		return err
	}

	defer unlock()

	names, err := appcontainer.Profiles()
	if err != nil {
		return err
	}

	failed := map[string]error{}

	for _, name := range names {
		if !hvservice.IsContainerName(name) || claimed(name) {
			continue
		}

		if err := appcontainer.DeleteProfile(name); err != nil {
			failed[name] = err
		}
	}

	if len(failed) == 0 {
		return nil
	}

	// a remove that failed as another remove had the profile under way, as
	// that of a run in another session, removed it all the same
	left, err := appcontainer.Profiles()
	if err != nil {
		return err
	}

	var errs []error

	for name, err := range failed {
		if slices.Contains(left, name) {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// sweepLock is the name of the mutex a sweep holds while it runs, which
// others skip: Windows answers a remove of a profile that another remove
// has under way with access denied, so two runs that start together would
// each report the leftovers the other removes. It is the session's, as a
// global name another user could take and hold, and keep every sweep from
// running: runs in two sessions may sweep at once, and at worst one says
// so.
const sweepLock = `Local\aibox.sweep`

// trySweep takes the lock of the sweeps and returns the function that
// lets it go, or nil when another sweep holds it. A run waits for no
// sweep, as a sweep is no reason to keep it from starting.
func trySweep() (func(), error) {
	n, err := windows.UTF16PtrFromString(sweepLock)
	if err != nil {
		return nil, err
	}

	h, err := windows.CreateMutex(nil, false, n)
	// held by an elevated run of the user, which this one may not open,
	// and whose sweep removes the same profiles
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, nil
	}

	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, fmt.Errorf("take the lock of the sweeps: %w", err)
	}

	// a mutex is the thread's that took it, and only that thread can let
	// it go: a goroutine that moved on to another would keep it held
	runtime.LockOSThread()

	// abandoned is a sweep whose aibox ended in it, and the mutex is this
	// one's all the same
	switch event, err := windows.WaitForSingleObject(h, 0); {
	case err == nil && (event == windows.WAIT_OBJECT_0 || event == windows.WAIT_ABANDONED):
		return func() {
			_ = windows.ReleaseMutex(h)

			runtime.UnlockOSThread()

			_ = windows.CloseHandle(h)
		}, nil
	case err == nil && event == uint32(windows.WAIT_TIMEOUT):
		runtime.UnlockOSThread()

		_ = windows.CloseHandle(h)

		return nil, nil
	default:
		runtime.UnlockOSThread()

		_ = windows.CloseHandle(h)

		return nil, fmt.Errorf("take the lock of the sweeps: %w", errors.Join(err, fmt.Errorf("event %d", event)))
	}
}
