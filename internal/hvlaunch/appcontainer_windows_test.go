package hvlaunch

import (
	"testing"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/appcontainer"
	"github.com/the127/aibox/internal/hvservice"
)

// aName is a name of a container as the service names one.
func aName(t *testing.T) string {
	t.Helper()

	id, err := guid.NewV4()
	require.NoError(t, err)

	return hvservice.ContainerName(id)
}

// aProfile makes the profile of the container of the name.
func aProfile(t *testing.T, name string) {
	t.Helper()

	_, err := appcontainer.CreateProfile(name)
	require.NoError(t, err)
	t.Cleanup(func() { _ = appcontainer.DeleteProfile(name) })
}

// Not parallel: a sweep skips while another runs.
func TestTheSweepRemovesAProfileNoRunHolds(t *testing.T) {
	// arrange: a run that made its profile and was killed, which let go
	// of its claim
	left := aName(t)

	held, err := claim(left)
	require.NoError(t, err)

	aProfile(t, left)
	require.NoError(t, windows.CloseHandle(held))

	// act
	err = sweepProfiles()

	// assert
	require.NoError(t, err)

	names, err := appcontainer.Profiles()
	require.NoError(t, err)
	assert.NotContains(t, names, left)
}

// Not parallel: a sweep skips while another runs.
func TestTheSweepKeepsTheProfileOfARun(t *testing.T) {
	// arrange: claimed before the profile is made, as a run does
	running := aName(t)

	held, err := claim(running)
	require.NoError(t, err)

	t.Cleanup(func() { _ = windows.CloseHandle(held) })

	aProfile(t, running)

	// act
	err = sweepProfiles()

	// assert
	require.NoError(t, err)

	names, err := appcontainer.Profiles()
	require.NoError(t, err)
	assert.Contains(t, names, running)
}

// Not parallel: a sweep skips while another runs.
func TestTheSweepKeepsTheProfilesOfOthers(t *testing.T) {
	// arrange: a name of a test, not of the service
	other := "aibox.test." + guid.GUID{}.String()

	_, err := appcontainer.CreateProfile(other)
	require.NoError(t, err)
	t.Cleanup(func() { _ = appcontainer.DeleteProfile(other) })

	// act
	err = sweepProfiles()

	// assert
	require.NoError(t, err)

	names, err := appcontainer.Profiles()
	require.NoError(t, err)
	assert.Contains(t, names, other)
}

func TestAClaimEndsWithItsHandle(t *testing.T) {
	t.Parallel()

	// arrange
	id, err := guid.NewV4()
	require.NoError(t, err)

	name := hvservice.ContainerName(id)

	held, err := claim(name)
	require.NoError(t, err)

	// act
	before := claimed(name)
	require.NoError(t, windows.CloseHandle(held))
	after := claimed(name)

	// assert
	assert.True(t, before)
	assert.False(t, after)
}

// Not parallel: a sweep skips while another runs.
func TestASweepSkipsWhileAnotherRuns(t *testing.T) {
	// arrange
	unlock, err := trySweep()
	require.NoError(t, err)
	require.NotNil(t, unlock)

	// act: from another goroutine, on a thread of its own
	other := func() bool {
		took := make(chan bool)

		go func() {
			u, err := trySweep()
			assert.NoError(t, err)

			if u != nil {
				u()
			}

			took <- u != nil
		}()

		return <-took
	}

	during := other()

	unlock()

	after := other()

	// assert
	assert.False(t, during, "the lock is held")
	assert.True(t, after, "the lock was let go")
}
