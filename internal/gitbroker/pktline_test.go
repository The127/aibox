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

// pushOfLength is the commands of a push, flush included, of exactly n
// bytes.
func pushOfLength(t *testing.T, n int) string {
	t.Helper()

	command := func(refBytes int) string {
		return pktLine(oldID + " " + newID + " refs/heads/aibox/" + strings.Repeat("a", refBytes) + "\n")
	}

	full := command(900)
	count := (n - len(flush)) / len(full)
	last := n - len(flush) - count*len(full)
	body := strings.Repeat(full, count) + command(last-len(command(0))) + flush
	require.Len(t, body, n)

	return body
}

func TestReadPushReadsCommandsUpToTheLimit(t *testing.T) {
	// arrange
	body := pushOfLength(t, maxPushCommandBytes)

	// act
	p, err := readPush(bufio.NewReader(strings.NewReader(body)))

	// assert
	require.NoError(t, err)
	assert.Len(t, p.raw, maxPushCommandBytes)
}

func TestReadPushRefusesCommandsBeyondTheLimit(t *testing.T) {
	// arrange: the broker keeps the commands to check them all, so a push
	// with more and more of them would fill the memory of aibox
	body := pushOfLength(t, maxPushCommandBytes+1)

	// act
	_, err := readPush(bufio.NewReader(strings.NewReader(body)))

	// assert
	assert.ErrorIs(t, err, errLongPush)
}

func TestAllowedUpdateChecksTheBranch(t *testing.T) {
	zero := zeroID(40)
	cases := map[string]struct {
		command command
		allowed bool
	}{
		"a matching branch":    {command{oldID, newID, "refs/heads/aibox/a"}, true},
		"a new branch":         {command{zero, newID, "refs/heads/aibox/a"}, true},
		"another branch":       {command{oldID, newID, "refs/heads/main"}, false},
		"a delete":             {command{oldID, zero, "refs/heads/aibox/a"}, false},
		"a tag":                {command{oldID, newID, "refs/tags/aibox/a"}, false},
		"a nested branch":      {command{oldID, newID, "refs/heads/aibox/a/b"}, false},
		"dot dot":              {command{oldID, newID, "refs/heads/aibox/..a"}, false},
		"a dot component":      {command{oldID, newID, "refs/heads/aibox/.a"}, false},
		"a lock file":          {command{oldID, newID, "refs/heads/aibox/a.lock"}, false},
		"a reflog expression":  {command{oldID, newID, "refs/heads/aibox/a@{1}"}, false},
		"a glob character":     {command{oldID, newID, "refs/heads/aibox/*"}, false},
		"another namespace":    {command{oldID, newID, "refs/heads/../aibox/a"}, false},
		"a space":              {command{oldID, newID, "refs/heads/aibox/a b"}, false},
		"a control character":  {command{oldID, newID, "refs/heads/aibox/a\x01b"}, false},
		"a delete character":   {command{oldID, newID, "refs/heads/aibox/a\x7fb"}, false},
		"a tilde":              {command{oldID, newID, "refs/heads/aibox/a~1"}, false},
		"a caret":              {command{oldID, newID, "refs/heads/aibox/a^1"}, false},
		"a colon":              {command{oldID, newID, "refs/heads/aibox/a:b"}, false},
		"a question mark":      {command{oldID, newID, "refs/heads/aibox/a?"}, false},
		"a bracket":            {command{oldID, newID, "refs/heads/aibox/a[b"}, false},
		"a backslash":          {command{oldID, newID, "refs/heads/aibox/a\\b"}, false},
		"a C1 control":         {command{oldID, newID, "refs/heads/aibox/a\u009b2J"}, false},
		"a reversing override": {command{oldID, newID, "refs/heads/aibox/a\u202eb"}, false},
		"an isolate":           {command{oldID, newID, "refs/heads/aibox/a\u2066b"}, false},
		"a zero width space":   {command{oldID, newID, "refs/heads/aibox/a\u200bb"}, false},
		"a no-break space":     {command{oldID, newID, "refs/heads/aibox/a\u00a0b"}, false},
		"a Persian joiner":     {command{oldID, newID, "refs/heads/aibox/\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645"}, true},
		"an emoji with joiner": {command{oldID, newID, "refs/heads/aibox/\U0001f469\u200d\U0001f4bb"}, true},
		"a line separator":     {command{oldID, newID, "refs/heads/aibox/a\u2028b"}, false},
		"no UTF-8":             {command{oldID, newID, "refs/heads/aibox/a\xffb"}, false},
		"letters of a script":  {command{oldID, newID, "refs/heads/aibox/f\u00fcr-gr\u00fc\u00dfe"}, true},
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

func TestWriteReportCutsALineTooLongForAPktLine(t *testing.T) {
	// arrange
	w := httptest.NewRecorder()
	ref := "refs/heads/" + strings.Repeat("a", maxPktLine)

	// act
	writeReport(w, []string{"report-status"}, []refusal{{ref, "no"}})

	// assert
	r := bufio.NewReader(w.Body)
	_, _, err := readPktLine(r)
	require.NoError(t, err)

	line, raw, err := readPktLine(r)
	require.NoError(t, err)
	assert.Len(t, raw, maxPktLine)
	assert.True(t, strings.HasPrefix(string(line), "ng refs/heads/aaa"))
	assert.True(t, strings.HasSuffix(string(line), "a\n"))

	line, _, err = readPktLine(r)
	require.NoError(t, err)
	assert.Nil(t, line)
}
