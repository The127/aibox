package ide

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeptHoldsAtMostItsLimit(t *testing.T) {
	// arrange
	k := newKept[int]()

	// act
	for i := range maxKept + 1 {
		k.put(strconv.Itoa(i), i)
	}

	// assert
	_, oldest := k.take("0")
	_, newest := k.take(strconv.Itoa(maxKept))
	assert.False(t, oldest, "the oldest entry is dropped")
	assert.True(t, newest)
	assert.Len(t, k.values, maxKept-1)
	assert.Len(t, k.order, maxKept-1)
}

func TestKeptDoesNotGrowWhenAKeyComesAndGoesAgain(t *testing.T) {
	// arrange: the VM can open and close a tab of one name again and again
	k := newKept[bool]()

	// act
	for range 10 * maxKept {
		k.put("tab", true)
		k.take("tab")
	}

	// assert
	assert.Empty(t, k.values)
	assert.Empty(t, k.order)
}

func TestKeptDropsTheOldestEvenAfterAKeyCameBack(t *testing.T) {
	// arrange
	k := newKept[bool]()
	k.put("reopened", true)
	k.take("reopened")

	for i := range maxKept - 1 {
		k.put(strconv.Itoa(i), true)
	}

	k.put("reopened", true)

	// act
	k.put("one more", true)

	// assert
	_, reopened := k.take("reopened")
	_, oldest := k.take("0")
	assert.True(t, reopened, "the reopened key is the newest but one")
	assert.False(t, oldest, "the oldest key is dropped")
}
