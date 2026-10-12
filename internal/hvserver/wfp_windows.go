package hvserver

import (
	"fmt"
	"slices"

	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hvservice"
)

// filterNetwork lets the container of the SID connect over TCP only to the
// ports of n, until the function it returns is called. The filters are in
// a session of the filtering engine of their own, which Windows ends, and
// them with it, when the service ends, however it ends.
func filterNetwork(sid string, n hvservice.Network) (func() error, error) {
	container, err := windows.StringToSid(sid)
	if err != nil {
		return nil, fmt.Errorf("read the SID of the container: %w", err)
	}

	session, err := wf.New(&wf.Options{Name: "aibox " + sid, Dynamic: true})
	if err != nil {
		return nil, fmt.Errorf("open the filtering engine: %w", err)
	}

	sublayer, err := windows.GenerateGUID()
	if err != nil {
		_ = session.Close()

		return nil, err
	}

	// a block in any sublayer wins over a permit, but a hard permit of a
	// sublayer weighed before ours would let the container through it
	if err := session.AddSublayer(&wf.Sublayer{ID: wf.SublayerID(sublayer), Name: "aibox " + sid, Weight: 0xFFFF}); err != nil {
		_ = session.Close()

		return nil, fmt.Errorf("add the sublayer of the container: %w", err)
	}

	rs, err := rules(wf.SublayerID(sublayer), container, n)
	if err != nil {
		_ = session.Close()

		return nil, err
	}

	for _, r := range rs {
		if err := session.AddRule(r); err != nil {
			_ = session.Close()

			return nil, fmt.Errorf("add the filter %s: %w", r.Name, err)
		}
	}

	return session.Close, nil
}

// The weights of the rules in the sublayer: a permit of a port weighs
// more than the block of everything else.
const (
	blockWeight  = 1
	permitWeight = 2
)

// rules are the filters of the container: a block of every connection it
// makes, over any protocol, and above it a permit of TCP to the ports of
// other hosts and one to the ports of the loopback. A port of one kind is
// no port of the other, so a port of the allow list doesn't open the same
// port of the loopback. The ports of a permit are its conditions, which
// the filtering engine ORs as they test the same field, merged into
// ranges: whatever the client asks for, the container gets at most six
// filters, rather than one for each port, of which the engine would hold
// and test some hundred thousand for each connection on the machine. The
// container connects to nothing else; as internetClient lets it make
// connections but take none, it needs no filter of what reaches it.
func rules(sublayer wf.SublayerID, container *windows.SID, n hvservice.Network) ([]*wf.Rule, error) {
	pkg := &wf.Match{Field: wf.FieldALEPackageID, Op: wf.MatchTypeEqual, Value: container}
	tcp := &wf.Match{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoTCP}
	remote := &wf.Match{Field: wf.FieldFlags, Op: wf.MatchTypeFlagsNoneSet, Value: wf.ConditionFlagIsLoopback}
	loopback := &wf.Match{Field: wf.FieldFlags, Op: wf.MatchTypeFlagsAllSet, Value: wf.ConditionFlagIsLoopback}

	var rs []*wf.Rule

	add := func(layer wf.LayerID, name string, weight uint64, action wf.Action, conditions ...*wf.Match) error {
		id, err := windows.GenerateGUID()
		if err != nil {
			return err
		}

		rs = append(rs, &wf.Rule{
			ID:         wf.RuleID(id),
			Name:       name,
			Layer:      layer,
			Sublayer:   sublayer,
			Weight:     weight,
			Conditions: append([]*wf.Match{pkg}, conditions...),
			Action:     action,
		})

		return nil
	}

	for _, layer := range []wf.LayerID{wf.LayerALEAuthConnectV4, wf.LayerALEAuthConnectV6} {
		if err := add(layer, "block", blockWeight, wf.ActionBlock); err != nil {
			return nil, err
		}

		if len(n.Ports) > 0 {
			if err := add(layer, "permit the ports", permitWeight, wf.ActionPermit, append([]*wf.Match{tcp, remote}, portMatches(n.Ports)...)...); err != nil {
				return nil, err
			}
		}

		if len(n.Loopback) > 0 {
			if err := add(layer, "permit the ports of the loopback", permitWeight, wf.ActionPermit, append([]*wf.Match{tcp, loopback}, portMatches(n.Loopback)...)...); err != nil {
				return nil, err
			}
		}
	}

	return rs, nil
}

// portMatches are the conditions that a connection goes to one of the
// ports, a range for each run of ports that follow one another.
func portMatches(ports []uint16) []*wf.Match {
	var matches []*wf.Match

	for _, r := range portRanges(ports) {
		matches = append(matches, &wf.Match{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeRange, Value: wf.Range{From: r[0], To: r[1]}})
	}

	return matches
}

// portRanges are the ports as runs of ports that follow one another, from
// the lowest, each as its first and its last.
func portRanges(ports []uint16) [][2]uint16 {
	sorted := slices.Clone(ports)
	slices.Sort(sorted)

	var ranges [][2]uint16

	for _, p := range sorted {
		if last := len(ranges) - 1; last >= 0 && (p == ranges[last][1] || p == ranges[last][1]+1) {
			ranges[last][1] = p

			continue
		}

		ranges = append(ranges, [2]uint16{p, p})
	}

	return ranges
}
