package hvserver

import (
	"fmt"
	"slices"
	"sync"
)

// loopbackList is the list of the containers Windows lets reach the
// loopback, one for the whole machine, by their SIDs.
type loopbackList interface {
	get() ([]string, error)
	set(sids []string) error
}

// loopbackRecord keeps the SIDs the service put on the list, where a
// service that crashed finds them again.
type loopbackRecord interface {
	load() ([]string, error)
	save(sids []string) error
}

// exemptions puts containers on the list and takes them off. The list is
// the machine's, so each change reads it and writes it whole, under mu,
// and leaves the entries of others as they were; the API has no way to
// change it otherwise, so a change another program makes at the same
// moment may be lost. active are the containers of the VMs that run.
type exemptions struct {
	list   loopbackList
	record loopbackRecord
	mu     sync.Mutex
	active map[string]bool
}

// exempt puts the container of the SID on the list, until the function
// it returns is called. The SID is recorded first, so that the service
// takes it off again after a crash, before Windows lets the container out.
// It first takes off what a removal that failed left, which no VM of the
// service holds any more, rather than leave it until the service starts
// again, which may be months away.
func (e *exemptions) exempt(sid string) (func() error, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.active == nil {
		e.active = map[string]bool{}
	}

	recorded, err := e.record.load()
	if err != nil {
		return nil, fmt.Errorf("read the containers on the loopback: %w", err)
	}

	var stale []string

	for _, s := range recorded {
		if !e.active[s] {
			stale = append(stale, s)
		}
	}

	// a container that could not be taken off stays recorded, for the
	// next try
	if len(stale) > 0 && e.removeLocked(stale) == nil {
		recorded = without(recorded, stale)
	}

	if err := e.record.save(appendOnce(recorded, sid)); err != nil {
		return nil, fmt.Errorf("record the container on the loopback: %w", err)
	}

	if err := e.add(sid); err != nil {
		// so that the record holds no container that is on no list
		_ = e.removeLocked([]string{sid})

		return nil, err
	}

	e.active[sid] = true

	return func() error { return e.remove([]string{sid}) }, nil
}

func (e *exemptions) add(sid string) error {
	list, err := e.list.get()
	if err != nil {
		return fmt.Errorf("read the containers Windows lets reach the loopback: %w", err)
	}

	if err := e.list.set(appendOnce(list, sid)); err != nil {
		return fmt.Errorf("let the container reach the loopback: %w", err)
	}

	return nil
}

// sweep takes the containers the record holds off the list: those of VMs
// of a service that ended without doing so.
func (e *exemptions) sweep() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	recorded, err := e.record.load()
	if err != nil {
		return fmt.Errorf("read the containers on the loopback: %w", err)
	}

	if len(recorded) == 0 {
		return nil
	}

	return e.removeLocked(recorded)
}

func (e *exemptions) remove(sids []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.removeLocked(sids)
}

// removeLocked takes the containers of the SIDs off the list, and then out
// of the record, so that a crash in between leaves them for the next
// sweep. A container whose VM ended is no active one, even if this fails,
// so that the next exemption tries again.
func (e *exemptions) removeLocked(sids []string) error {
	for _, s := range sids {
		delete(e.active, s)
	}

	list, err := e.list.get()
	if err != nil {
		return fmt.Errorf("read the containers Windows lets reach the loopback: %w", err)
	}

	if err := e.list.set(without(list, sids)); err != nil {
		return fmt.Errorf("take the containers off the loopback: %w", err)
	}

	recorded, err := e.record.load()
	if err != nil {
		return fmt.Errorf("read the containers on the loopback: %w", err)
	}

	return e.record.save(without(recorded, sids))
}

// appendOnce is the list with the SID, which it holds once.
func appendOnce(list []string, sid string) []string {
	if slices.Contains(list, sid) {
		return list
	}

	return append(slices.Clone(list), sid)
}

// without is the list less the SIDs.
func without(list, sids []string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(s string) bool { return slices.Contains(sids, s) })
}
