package gitbroker

import (
	"bufio"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	oldID = "1111111111111111111111111111111111111111"
	newID = "2222222222222222222222222222222222222222"
)

func TestReadPushReadsTheCommandsAndLeavesThePack(t *testing.T) {
	// arrange
	start := pktLine(oldID+" "+newID+" refs/heads/aibox/a\x00report-status side-band-64k\n") + pktLine(oldID+" "+newID+" refs/heads/aibox/b\n") + flush
	r := bufio.NewReader(strings.NewReader(start + "PACK..."))

	// act
	p, err := readPush(r)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []command{{oldID, newID, "refs/heads/aibox/a"}, {oldID, newID, "refs/heads/aibox/b"}}, p.commands)
	assert.Equal(t, []string{"report-status", "side-band-64k"}, p.capabilities)
	assert.Equal(t, start, string(p.raw))

	rest, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "PACK...", string(rest))
}

func TestReadPushRefusesWhatItCannotCheck(t *testing.T) {
	cases := map[string]struct {
		body string
		err  error
	}{
		"a shallow push":           {pktLine("shallow "+oldID+"\n") + pktLine(oldID+" "+newID+" refs/heads/a\n") + flush, errShallow},
		"a signed push":            {pktLine("push-cert\x00report-status\n") + flush, errPushCert},
		"push options":             {pktLine(oldID+" "+newID+" refs/heads/a\x00report-status push-options\n") + flush, errPushOptions},
		"no commands":              {flush, errNoCommands},
		"no flush":                 {pktLine(oldID + " " + newID + " refs/heads/a\n"), errBadPktLine},
		"a short line":             {"0004" + flush, errBadPktLine},
		"a delimiter":              {"0001", errBadPktLine},
		"upper case hex":           {"003F" + oldID + " " + newID + " refs/heads/a" + flush, errBadPktLine},
		"a line that is too long":  {"fff1" + strings.Repeat("a", 0xfff1-4), errBadPktLine},
		"not a hex length":         {"zzzz", errBadPktLine},
		"a space in the ref":       {pktLine(oldID+" "+newID+" refs/heads/a b\n") + flush, errBadPktLine},
		"two lengths of object ID": {pktLine(oldID+" "+strings.Repeat("2", 64)+" refs/heads/a\n") + flush, errMixedObjects},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			// act
			_, err := readPush(bufio.NewReader(strings.NewReader(c.body)))

			// assert
			assert.ErrorIs(t, err, c.err)
		})
	}
}

func TestAllowedUpdateChecksTheBranch(t *testing.T) {
	zero := zeroID(40)
	cases := map[string]struct {
		command command
		allowed bool
	}{
		"a matching branch":   {command{oldID, newID, "refs/heads/aibox/a"}, true},
		"a new branch":        {command{zero, newID, "refs/heads/aibox/a"}, true},
		"another branch":      {command{oldID, newID, "refs/heads/main"}, false},
		"a delete":            {command{oldID, zero, "refs/heads/aibox/a"}, false},
		"a tag":               {command{oldID, newID, "refs/tags/aibox/a"}, false},
		"a nested branch":     {command{oldID, newID, "refs/heads/aibox/a/b"}, false},
		"dot dot":             {command{oldID, newID, "refs/heads/aibox/..a"}, false},
		"a dot component":     {command{oldID, newID, "refs/heads/aibox/.a"}, false},
		"a lock file":         {command{oldID, newID, "refs/heads/aibox/a.lock"}, false},
		"a reflog expression": {command{oldID, newID, "refs/heads/aibox/a@{1}"}, false},
		"a glob character":    {command{oldID, newID, "refs/heads/aibox/*"}, false},
		"another namespace":   {command{oldID, newID, "refs/heads/../aibox/a"}, false},
		"a space":             {command{oldID, newID, "refs/heads/aibox/a b"}, false},
		"a control character": {command{oldID, newID, "refs/heads/aibox/a\x01b"}, false},
		"a delete character":  {command{oldID, newID, "refs/heads/aibox/a\x7fb"}, false},
		"a tilde":             {command{oldID, newID, "refs/heads/aibox/a~1"}, false},
		"a caret":             {command{oldID, newID, "refs/heads/aibox/a^1"}, false},
		"a colon":             {command{oldID, newID, "refs/heads/aibox/a:b"}, false},
		"a question mark":     {command{oldID, newID, "refs/heads/aibox/a?"}, false},
		"a bracket":           {command{oldID, newID, "refs/heads/aibox/a[b"}, false},
		"a backslash":         {command{oldID, newID, "refs/heads/aibox/a\\b"}, false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			// act
			reason := allowedUpdate(c.command, []string{"aibox/*"})

			// assert
			assert.Equal(t, c.allowed, reason == "", reason)
		})
	}
}

func TestWriteReportPutsTheReportInTheSideBand(t *testing.T) {
	// arrange
	w := httptest.NewRecorder()

	// act
	writeReport(w, []string{"report-status", "side-band-64k"}, []refusal{{"refs/heads/main", "no"}})

	// assert
	inner := pktLine("unpack ok\n") + pktLine("ng refs/heads/main aibox: no\n") + flush
	assert.Equal(t, pktLine("\x01"+inner)+flush, w.Body.String())
	assert.Equal(t, "application/x-git-receive-pack-result", w.Header().Get("Content-Type"))
}

func TestWriteReportWithoutSideBand(t *testing.T) {
	// arrange
	w := httptest.NewRecorder()

	// act
	writeReport(w, []string{"report-status"}, []refusal{{"refs/heads/main", "no"}})

	// assert
	assert.Equal(t, pktLine("unpack ok\n")+pktLine("ng refs/heads/main aibox: no\n")+flush, w.Body.String())
}
