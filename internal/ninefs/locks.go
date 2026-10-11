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
}

// lockEnd is where a lock of the length from start ends. A length of 0
// locks to the end of the file, however far it grows.
func lockEnd(start, length uint64) uint64 {
	if length == 0 || start > math.MaxUint64-length {
		return math.MaxUint64
	}

	return start + length
}

// set takes, changes or, with neither read nor write, releases the owner's
// lock on the range of the file. It reports false, and changes nothing,
// when another owner's lock is in the way.
func (t *lockTable[K]) set(file K, owner lockOwner, read, write bool, start, length uint64) bool {
	end := lockEnd(start, length)

	t.mu.Lock()
	defer t.mu.Unlock()

	locks := t.files[file]

	if read || write {
		for _, l := range locks {
			if l.owner != owner && l.start < end && start < l.end && (l.write || write) {
				return false
			}
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

	return true
}
