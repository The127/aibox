package hvserver

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeList is the list of the machine, which setErr fails to change.
type fakeList struct {
	sids   []string
	setErr error
}

func (l *fakeList) get() ([]string, error) { return l.sids, nil }

func (l *fakeList) set(sids []string) error {
	if l.setErr != nil {
		return l.setErr
	}

	l.sids = sids

	return nil
}

type fakeRecord struct{ sids []string }

func (r *fakeRecord) load() ([]string, error) { return r.sids, nil }

func (r *fakeRecord) save(sids []string) error {
	r.sids = sids

	return nil
}

// someoneElses is a container another program put on the list.
const someoneElses = "S-1-15-2-9-9-9-9-9-9-9"

func TestAContainerIsOnTheLoopbackUntilItIsTakenOff(t *testing.T) {
	// arrange
	list, record := &fakeList{sids: []string{someoneElses}}, &fakeRecord{}
	e := &exemptions{list: list, record: record}

	// act
	unexempt, err := e.exempt(aContainerSID)
	require.NoError(t, err)

	during, recorded := list.sids, record.sids

	// assert
	assert.Equal(t, []string{someoneElses, aContainerSID}, during)
	assert.Equal(t, []string{aContainerSID}, recorded)

	require.NoError(t, unexempt())
	assert.Equal(t, []string{someoneElses}, list.sids, "the other's entry stays")
	assert.Empty(t, record.sids)
}

func TestAContainerIsRecordedBeforeWindowsLetsItOut(t *testing.T) {
	// arrange: the list can't be changed, as by a crash right after the
	// record
	list, record := &fakeList{setErr: errors.New("the firewall said no")}, &fakeRecord{}
	e := &exemptions{list: list, record: record}

	// act
	_, err := e.exempt(aContainerSID)

	// assert
	require.Error(t, err)
	assert.Equal(t, []string{aContainerSID}, record.sids, "for the next sweep")
}

func TestTheSweepTakesOffWhatTheRecordHoldsAndNothingElse(t *testing.T) {
	// arrange: a service that crashed left a container on the list
	list := &fakeList{sids: []string{someoneElses, aContainerSID}}
	record := &fakeRecord{sids: []string{aContainerSID}}
	e := &exemptions{list: list, record: record}

	// act
	err := e.sweep()

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{someoneElses}, list.sids)
	assert.Empty(t, record.sids)
}

func TestAContainerIsOnTheListOnce(t *testing.T) {
	// arrange
	list := &fakeList{sids: []string{aContainerSID}}
	e := &exemptions{list: list, record: &fakeRecord{}}

	// act
	_, err := e.exempt(aContainerSID)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{aContainerSID}, list.sids)
}

func TestARemovalThatFailedIsTriedAgainAtTheNextExemption(t *testing.T) {
	// arrange: the container of a VM that ended, whose removal failed
	list, record := &fakeList{}, &fakeRecord{}
	e := &exemptions{list: list, record: record}

	unexempt, err := e.exempt(aContainerSID)
	require.NoError(t, err)

	list.setErr = errors.New("the firewall said no")
	require.Error(t, unexempt())

	list.setErr = nil

	// act: the next VM's container
	_, err = e.exempt(someoneElses)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{someoneElses}, list.sids, "the first one is off the list")
	assert.Equal(t, []string{someoneElses}, record.sids)
}
