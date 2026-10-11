package ninefs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	alice = lockOwner{client: "guest", pid: 1}
	bob   = lockOwner{client: "guest", pid: 2}
)

func TestReadLocksOfTwoOwnersShareARange(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, table.set("db", alice, true, false, 0, 100))

	// act
	ok := table.set("db", bob, true, false, 50, 100)

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
		"a read inside":          {false, 10, 5, false},
		"a write overlapping":    {true, 90, 20, false},
		"a write right after":    {true, 100, 10, true},
		"a read to the end":      {false, 99, 0, false},
		"a write before, adjoin": {true, 0, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			var table lockTable[string]
			assert.True(t, table.set("db", alice, false, true, 0, 100))

			// act
			ok := table.set("db", bob, !other.write, other.write, other.start, other.length)

			// assert
			assert.Equal(t, other.ok, ok)
		})
	}
}

func TestAnOwnerNeverConflictsWithItself(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, table.set("db", alice, true, false, 0, 100))

	// act: a read lock turned into a write lock, as SQLite does
	ok := table.set("db", alice, false, true, 0, 100)

	// assert
	assert.True(t, ok)
	assert.False(t, table.set("db", bob, true, false, 0, 1))
}

func TestAnUnlockOfPartOfARangeLeavesTheRest(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, table.set("db", alice, false, true, 0, 100))

	// act
	table.set("db", alice, false, false, 40, 20)

	// assert
	assert.True(t, table.set("db", bob, false, true, 40, 20))
	assert.False(t, table.set("db", bob, false, true, 39, 1))
	assert.False(t, table.set("db", bob, false, true, 60, 1))
}

func TestAnUnlockOfEverythingForgetsTheFile(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, table.set("db", alice, false, true, 0, 100))
	assert.True(t, table.set("db", alice, true, false, 200, 0))

	// act
	table.set("db", alice, false, false, 0, 0)

	// assert
	assert.Empty(t, table.files)
}

func TestLocksOfDifferentFilesNeverMeet(t *testing.T) {
	t.Parallel()

	// arrange
	var table lockTable[string]
	assert.True(t, table.set("a", alice, false, true, 0, 0))

	// act
	ok := table.set("b", bob, false, true, 0, 0)

	// assert
	assert.True(t, ok)
}

func TestALockToTheEndOfTheFileDoesNotOverflow(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint64(1<<64-1), lockEnd(1<<64-10, 100))
	assert.Equal(t, uint64(1<<64-1), lockEnd(5, 0))
	assert.Equal(t, uint64(15), lockEnd(5, 10))
}
