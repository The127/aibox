package hvserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hvservice"
)

func TestTheRulesBlockTheContainerButForItsPorts(t *testing.T) {
	t.Parallel()

	// arrange
	sid, err := windows.StringToSid("S-1-15-2-1-2-3-4-5-6-7")
	require.NoError(t, err)

	sublayer := wf.SublayerID{Data1: 1}

	// act
	rs, err := rules(sublayer, sid, hvservice.Network{Ports: []uint16{443, 22}, Loopback: []uint16{63342}})

	// assert
	require.NoError(t, err)
	require.Len(t, rs, 2*3, "for IPv4 and IPv6: the block, the ports, those of the loopback")

	for _, r := range rs {
		assert.Equal(t, sublayer, r.Sublayer)
		require.NotEmpty(t, r.Conditions)
		assert.Equal(t, wf.FieldALEPackageID, r.Conditions[0].Field, "of the container only")
		assert.Equal(t, sid, r.Conditions[0].Value)

		switch r.Action {
		case wf.ActionBlock:
			assert.Len(t, r.Conditions, 1, "everything else")
			assert.Equal(t, uint64(blockWeight), r.Weight)
		case wf.ActionPermit:
			assert.GreaterOrEqual(t, len(r.Conditions), 4, "the container, TCP, whether it is of the loopback, and its ports")
			assert.Greater(t, r.Weight, uint64(blockWeight))
		default:
			t.Errorf("a rule that neither blocks nor permits: %v", r.Action)
		}
	}
}

func TestTheFiltersOfAContainerGoWhenTheyAreTakenAway(t *testing.T) {
	t.Parallel()

	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("the filtering engine takes filters from an administrator only")
	}

	// arrange
	sid, err := hvservice.ContainerSID("aibox.test.filters")
	require.NoError(t, err)

	// act
	// every port there is, which is still a filter for each list
	every := make([]uint16, 0, 65535)
	for p := 1; p <= 65535; p++ {
		every = append(every, uint16(p))
	}

	unfilter, err := filterNetwork(sid, hvservice.Network{Ports: every, Loopback: []uint16{63342}})
	require.NoError(t, err)

	// assert
	assert.NoError(t, unfilter())
}

func TestPortsMergeIntoRanges(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		ports []uint16
		want  [][2]uint16
	}{
		"one":                    {[]uint16{443}, [][2]uint16{{443, 443}}},
		"apart":                  {[]uint16{443, 22}, [][2]uint16{{22, 22}, {443, 443}}},
		"one after another":      {[]uint16{8081, 8080, 8082}, [][2]uint16{{8080, 8082}}},
		"the highest and lowest": {[]uint16{65535, 1, 65534}, [][2]uint16{{1, 1}, {65534, 65535}}},
		"every other":            {[]uint16{1, 3, 5}, [][2]uint16{{1, 1}, {3, 3}, {5, 5}}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, portRanges(tt.ports))
		})
	}
}
