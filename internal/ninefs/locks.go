package ninefs

import (
	"math"
	"sync"
)

// lockOwner is who holds a POSIX record lock: a process of a client of the
// server. A process's locks never conflict with each other.
type lockOwner struct {
	client string
	pid    int32
}

// lockRange is a lock on the bytes from start up to end, end excluded.
type lockRange struct {
	owner      lockOwner
	start, end uint64
	write      bool
}

// lockTable holds the POSIX record locks of the guest, by file. Windows'
// own byte-range locks are mandatory, so they would make the reads and
// writes of others fail, where a POSIX lock only keeps other lockers out;
// the guest's locks need to agree among the guest's processes only.
type lockTable[K comparable] struct {
	mu    sync.Mutex
	files map[K][]lockRange
	// changed is closed at the next change of the table, for locks that
	// wait for one in their way to go
	changed chan struct{}
}

// lockEnd is where a lock of the length from start ends. A length of 0
// locks to the end of the file, however far it grows.
func lockEnd(start, length uint64) uint64 {
	if length == 0 || start > math.MaxUint64-length {
		return math.MaxUint64
	}

	return start + length
}

// try takes, changes or, with neither read nor write, releases the owner's
// lock on the range of the file. When another owner's lock is in the way,
// it changes nothing and returns false and a channel that is closed at the
// next change of the table, taken before anything else can change it, so
// that no release goes unnoticed.
func (t *lockTable[K]) try(file K, owner lockOwner, read, write bool, start, length uint64) (bool, <-chan struct{}) {
	end := lockEnd(start, length)

	t.mu.Lock()
	defer t.mu.Unlock()

	locks := t.files[file]

	if read || write {
		if _, ok := firstConflict(locks, owner, write, start, end); ok {
			if t.changed == nil {
				t.changed = make(chan struct{})
			}

			return false, t.changed
		}
	}

	// the owner's locks in the range give way to the new one, or to none,
	// as a POSIX lock replaces what the process held there
	kept := make([]lockRange, 0, len(locks)+2)

	for _, l := range locks {
		if l.owner != owner || l.end <= start || end <= l.start {
			kept = append(kept, l)

			continue
		}

		if l.start < start {
			kept = append(kept, lockRange{owner: l.owner, start: l.start, end: start, write: l.write})
		}

		if end < l.end {
			kept = append(kept, lockRange{owner: l.owner, start: end, end: l.end, write: l.write})
		}
	}

	if read || write {
		kept = append(kept, lockRange{owner: owner, start: start, end: end, write: write})
	}

	if t.files == nil {
		t.files = map[K][]lockRange{}
	}

	if len(kept) == 0 {
		delete(t.files, file)
	} else {
		t.files[file] = kept
	}

	if t.changed != nil {
		close(t.changed)
		t.changed = nil
	}

	return true, nil
}

// conflict is a lock of another owner in the way of the owner's lock on
// the range, if there is one.
func (t *lockTable[K]) conflict(file K, owner lockOwner, write bool, start, length uint64) (lockRange, bool) {
	end := lockEnd(start, length)

	t.mu.Lock()
	defer t.mu.Unlock()

	return firstConflict(t.files[file], owner, write, start, end)
}

// firstConflict is the first of the locks in the way of the owner's lock
// on the bytes from start up to end, if one is.
func firstConflict(locks []lockRange, owner lockOwner, write bool, start, end uint64) (lockRange, bool) {
	for _, l := range locks {
		if l.conflicts(owner, write, start, end) {
			return l, true
		}
	}

	return lockRange{}, false
}

// conflicts tells whether the lock keeps the owner from a lock on the
// bytes from start up to end: a lock of another owner on some of them,
// one of the two to write.
func (l lockRange) conflicts(owner lockOwner, write bool, start, end uint64) bool {
	return l.owner != owner && l.start < end && start < l.end && (l.write || write)
}
