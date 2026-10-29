package gitbroker

import (
	"bufio"
	"bytes"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

var (
	objectID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	refName  = regexp.MustCompile(`^refs/[^\x00-\x20\x7f]+$`)
)

// FuzzReadPktLine reads pkt-lines the VM, or a server over SSH, could
// send. A line readPktLine takes is the bytes its header counts, no more
// than git sends, and a flush is the four bytes alone.
func FuzzReadPktLine(f *testing.F) {
	f.Add([]byte("0009hello0000"))
	f.Add([]byte("0000"))
	f.Add([]byte("0004"))
	f.Add([]byte("fff1" + strings.Repeat("a", 10)))
	f.Add([]byte("00zz"))

	f.Fuzz(func(t *testing.T, data []byte) {
		line, raw, err := readPktLine(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return
		}

		if !bytes.HasPrefix(data, raw) {
			t.Fatalf("raw %q is not what came", raw)
		}

		if line == nil {
			if string(raw) != flush {
				t.Fatalf("a flush of %q", raw)
			}

			return
		}

		length, err := strconv.ParseUint(string(raw[:4]), 16, 16)
		if err != nil || int(length) != len(raw) || len(raw) > maxPktLine || !bytes.Equal(line, raw[4:]) {
			t.Fatalf("line %q of raw %q", line, raw)
		}
	})
}

// FuzzReadPush reads the start of a push from the VM. A push readPush
// takes updates refs by full object IDs of one length, ends with a flush,
// keeps no more than maxPushCommandBytes and carries nothing it can't
// check.
func FuzzReadPush(f *testing.F) {
	old, next := strings.Repeat("1", 40), strings.Repeat("2", 40)
	f.Add([]byte(pktLine(old+" "+next+" refs/heads/aibox/a\x00report-status side-band-64k\n") + pktLine(old+" "+next+" refs/heads/aibox/b\n") + flush + "PACK"))
	f.Add([]byte(pktLine("shallow "+old+"\n") + flush))
	f.Add([]byte(pktLine(old+" "+next+" refs/heads/a\x00push-options\n") + flush))
	f.Add([]byte(flush))

	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := readPush(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return
		}

		switch {
		case len(p.commands) == 0:
			t.Fatal("took a push that updates nothing")
		case !bytes.HasPrefix(data, p.raw) || !bytes.HasSuffix(p.raw, []byte(flush)):
			t.Fatalf("raw %q", p.raw)
		case len(p.raw) > maxPushCommandBytes:
			t.Fatalf("kept %d bytes", len(p.raw))
		}

		for _, c := range p.commands {
			if !objectID.MatchString(c.old) || !objectID.MatchString(c.new) || len(c.old) != len(c.new) || !refName.MatchString(c.ref) {
				t.Fatalf("took the command %+v", c)
			}
		}

		for _, capability := range p.capabilities {
			if capability == "push-options" {
				t.Fatal("took push options")
			}
		}
	})
}

// FuzzPktLine writes texts as pkt-lines, as the broker does when it tells
// the VM why it refused a push. What it writes is a pkt-line git reads,
// and the text comes back, cut only where it doesn't fit.
func FuzzPktLine(f *testing.F) {
	f.Add("ng refs/heads/main the config allows pushes only to aibox/*\n")
	f.Add("x")

	f.Fuzz(func(t *testing.T, text string) {
		if text == "" {
			return
		}

		written := pktLine(text)
		if len(written) > maxPktLine {
			t.Fatalf("wrote %d bytes", len(written))
		}

		line, _, err := readPktLine(bufio.NewReader(strings.NewReader(written)))
		if err != nil {
			t.Fatalf("wrote %q, which reads as %v", written, err)
		}

		if len(text)+4 <= maxPktLine && string(line) != text {
			t.Fatalf("%q came back as %q", text, line)
		}

		if strings.HasSuffix(text, "\n") && !bytes.HasSuffix(line, []byte("\n")) {
			t.Fatalf("lost the newline of %q", text)
		}
	})
}

// FuzzParse reads requests from the VM. A request parse takes names a
// service of git and a remote whose path is plain, with no part that is
// .. and no escapes, so that no server reads it as another repository.
func FuzzParse(f *testing.F) {
	f.Add(http.MethodGet, "/github.com/owner/repo.git/info/refs?service=git-upload-pack")
	f.Add(http.MethodPost, "/github.com/owner/repo/git-receive-pack")
	f.Add(http.MethodPost, "/github.com/owner/../other/git-receive-pack")
	f.Add(http.MethodGet, "/github.com/owner/repo/info/refs?service=git-upload-pack&x=1")
	f.Add(http.MethodPost, "/github.com/owner/repo%2f..%2fx/git-upload-pack")

	f.Fuzz(func(t *testing.T, method, target string) {
		u, err := url.ParseRequestURI(target)
		if err != nil {
			return
		}

		req, ok := parse(&http.Request{Method: method, URL: u})
		if !ok {
			return
		}

		switch {
		case req.remote == "" || slices.Contains(strings.Split(req.remote, "/"), "..") || strings.HasPrefix(req.remote, "/"):
			t.Fatalf("took the remote %q of %q", req.remote, target)
		case !gitPath.MatchString("/"+req.remote) || path.Clean("/"+req.remote) != "/"+req.remote:
			t.Fatalf("took the remote %q of %q", req.remote, target)
		case req.service != uploadPack && req.service != receivePack:
			t.Fatalf("took the service %q of %q", req.service, target)
		case method != http.MethodGet && method != http.MethodPost:
			t.Fatalf("took the method %q", method)
		}
	})
}

// FuzzAllowedUpdate checks the refs a push from the VM updates. A ref
// allowedUpdate allows is a branch, not deleted, with a name git takes,
// that a pattern in the config matches.
func FuzzAllowedUpdate(f *testing.F) {
	f.Add("refs/heads/aibox/fix", "aibox/*", false)
	f.Add("refs/heads/main", "*", true)
	f.Add("refs/tags/v1", "*", false)
	f.Add("refs/heads/aibox/../main", "aibox/*", false)
	f.Add("refs/heads/aibox/a.lock", "aibox/*", false)

	f.Fuzz(func(t *testing.T, ref, pattern string, deleting bool) {
		next := strings.Repeat("2", 40)
		if deleting {
			next = zeroID(40)
		}

		if allowedUpdate(command{old: strings.Repeat("1", 40), new: next, ref: ref}, []string{pattern}) != "" {
			return
		}

		branch, isBranch := strings.CutPrefix(ref, "refs/heads/")
		matched, _ := path.Match(pattern, branch)

		switch {
		case !isBranch || deleting:
			t.Fatalf("allowed %q, deleting %v", ref, deleting)
		case !validBranch(branch) || strings.Contains(branch, "..") || strings.ContainsFunc(branch, unicode.IsControl):
			t.Fatalf("allowed the branch %q", branch)
		case !matched:
			t.Fatalf("allowed %q, which %q doesn't match", branch, pattern)
		}
	})
}

// FuzzSanitize cleans text from the VM before it goes into the log.
// Nothing a terminal acts on comes out: no control character, no newline
// and nothing that reverses or isolates the direction of text.
func FuzzSanitize(f *testing.F) {
	f.Add("refused push to github.com/o/r: refs/heads/\x1b[2Jmain")
	f.Add("a\u202eb\nc\u009b2J\u2066")

	f.Fuzz(func(t *testing.T, text string) {
		for _, r := range sanitize(text) {
			if r < ' ' || r >= 0x7f && r <= 0x9f || r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069' || r == '\u2028' || r == '\u2029' {
				t.Fatalf("%q kept %U", text, r)
			}
		}
	})
}
