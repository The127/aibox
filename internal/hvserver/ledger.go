// Package hvserver is the Windows service of aibox, which runs as SYSTEM
// and makes the VMs of the people who run aibox over the pipe of
// hvservice. It trusts nothing of a request it can check.
package hvserver

import (
	"errors"
	"fmt"
	"sync"
)

// ErrNoMemory is what reserve returns for a VM that would take more
// memory than the host has left over the VMs that run.
var ErrNoMemory = errors.New("the host has too little memory left")

// errNoVM is a reservation of no memory, which no VM that passed Check
// asks for; one less than none would add to what the others may take.
var errNoVM = errors.New("a VM of no memory")

// ledger is the memory the VMs that run may take. Hyper-V lets them
// promise more than the host has and backs them as they use it, so it
// would let VMs together take more than the host can back; the ledger
// keeps them to what it can.
type ledger struct {
	mu   sync.Mutex
	used int
}

// reserve takes the memory of a VM, in MiB, out of the total the host has,
// or refuses it if too little is left. release gives it back, once
// however often it is called.
func (l *ledger) reserve(mib, total int) (release func(), err error) {
	if mib < 1 {
		return nil, fmt.Errorf("%w: %d MiB", errNoVM, mib)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// a host that reads less memory than before may be below what runs
	// already, and takes no VM until enough of them have ended
	if mib > total-l.used {
		return nil, fmt.Errorf("%w: %d of %d MiB in use, too little for %d MiB", ErrNoMemory, l.used, total, mib)
	}

	l.used += mib

	var once sync.Once

	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()

			l.used -= mib
		})
	}, nil
}
