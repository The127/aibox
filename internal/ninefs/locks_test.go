package ninefs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	alice = lockOwner{client: "guest", pid: 1}
	bob   = lockOwner{client: "guest", pid: 2}
)

// set is try for a test that has no wait for the lock in its way.
func set[K comparable](table *lockTable[K], file K, owner lockOwner, read, write bool, start, length uint64) bool {
	ok, _ := table.try(file, owner, read, write, start, length)

	return ok
}

func TestReadLocksOfTwoOwnersShareARange(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, set(&table, "db", alice, true, false, 0, 100))

	// act
	ok := set(&table, "db", bob, true, false, 50, 100)

	// assert
	assert.True(t, ok)
}

func TestAWriteLockKeepsOtherOwnersOutOfItsRange(t *testing.T) {
	t.Parallel()

	for name, other := range map[string]struct {
		write         bool
		start, length uint64
		ok            bool
	}{
		"a read inside":       {false, 10, 5, false},
		"a write overlapping": {true, 90, 20, false},
		"a write right after": {true, 100, 10, true},
		"a read to the end":   {false, 99, 0, false},
		"a write to the end":  {true, 0, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			var table lockTable[string]
			assert.True(t, set(&table, "db", alice, false, true, 0, 100))

			// act
			ok := set(&table, "db", bob, !other.write, other.write, other.start, other.length)

			// assert
			assert.Equal(t, other.ok, ok)
		})
	}
}

func TestAnOwnerNeverConflictsWithItself(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, set(&table, "db", alice, true, false, 0, 100))

	// act: a read lock turned into a write lock, as SQLite does
	ok := set(&table, "db", alice, false, true, 0, 100)

	// assert
	assert.True(t, ok)
	assert.False(t, set(&table, "db", bob, true, false, 0, 1))
}

func TestAnUnlockOfPartOfARangeLeavesTheRest(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, set(&table, "db", alice, false, true, 0, 100))

	// act
	set(&table, "db", alice, false, false, 40, 20)

	// assert
	assert.True(t, set(&table, "db", bob, false, true, 40, 20))
	assert.False(t, set(&table, "db", bob, false, true, 39, 1))
	assert.False(t, set(&table, "db", bob, false, true, 60, 1))
}

func TestAnUnlockOfEverythingForgetsTheFile(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, set(&table, "db", alice, false, true, 0, 100))
	assert.True(t, set(&table, "db", alice, true, false, 200, 0))

	// act
	set(&table, "db", alice, false, false, 0, 0)

	// assert
	assert.Empty(t, table.files)
}

func TestLocksOfDifferentFilesNeverMeet(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, set(&table, "a", alice, false, true, 0, 0))

	// act
	ok := set(&table, "b", bob, false, true, 0, 0)

	// assert
	assert.True(t, ok)
}

func TestALockToTheEndOfTheFileDoesNotOverflow(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint64(1<<64-1), lockEnd(1<<64-10, 100))
	assert.Equal(t, uint64(1<<64-1), lockEnd(5, 0))
	assert.Equal(t, uint64(15), lockEnd(5, 10))
}

func TestConflictFindsOnlyAnotherOwnersLockThatMatters(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, set(&table, "db", alice, true, false, 0, 10))

	// act
	_, readerBlocked := table.conflict("db", bob, false, 0, 10)
	l, writerBlocked := table.conflict("db", bob, true, 5, 1)
	_, selfBlocked := table.conflict("db", alice, true, 0, 10)

	// assert
	assert.False(t, readerBlocked)
	assert.True(t, writerBlocked)
	assert.Equal(t, lockRange{owner: alice, start: 0, end: 10}, l)
	assert.False(t, selfBlocked)
}

func TestTryHandsOutAChannelThatClosesAtTheNextChange(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, set(&table, "db", alice, false, true, 0, 0))

	ok, changed := table.try("db", bob, false, true, 0, 0)
	assert.False(t, ok)

	// act
	set(&table, "db", alice, false, false, 0, 0)

	// assert
	select {
	case <-changed:
	default:
		t.Fatal("the channel did not close when the lock went")
	}

	ok, _ = table.try("db", bob, false, true, 0, 0)
	assert.True(t, ok)
}
