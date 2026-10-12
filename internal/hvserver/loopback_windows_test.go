package hvserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hvservice"
)

// The list is the machine's: the test puts a container on it and takes it
// off, and leaves the list as it found it.
func TestTheListOfTheMachineTakesAContainerAndGivesItBack(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("Windows lets an administrator only change who reaches the loopback")
	}

	// arrange
	list := isolationList{}

	before, err := list.get()
	require.NoError(t, err)

	// the list is the machine's, so a test that fails leaves it as it was
	t.Cleanup(func() { _ = list.set(before) })

	sid, err := hvservice.ContainerSID("aibox.test.loopback")
	require.NoError(t, err)

	e := &exemptions{list: list, record: &fakeRecord{}}

	// act
	unexempt, err := e.exempt(sid)
	require.NoError(t, err)

	during, err := list.get()
	require.NoError(t, err)

	undoErr := unexempt()

	after, err := list.get()
	require.NoError(t, err)

	// assert
	require.NoError(t, undoErr)
	assert.Contains(t, during, sid)
	assert.ElementsMatch(t, before, after)
}
