package gitbroker

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A push starts with pkt-lines: four hex digits that give the length of the
// line with themselves, then the line. Each line updates one ref, and the
// first carries the capabilities of the client after a NUL. A flush, 0000,
// ends them, and the pack follows.
const (
	flush = "0000"
	// maxPktLine is the longest pkt-line git sends.
	maxPktLine = 65520
	// maxCommands is how many refs one push may update.
	maxCommands = 1000
)

var (
	errBadPktLine   = errors.New("the push is not in the format of git")
	errShallow      = errors.New("a push from a shallow clone, which aibox does not pass on")
	errPushCert     = errors.New("a signed push, which aibox does not pass on")
	errPushOptions  = errors.New("push options, which aibox does not pass on")
	errTooManyRefs  = fmt.Errorf("more than %d refs in one push", maxCommands)
	errNoCommands   = errors.New("a push that updates nothing")
	errMixedObjects = errors.New("object IDs of two lengths in one command")
)

// command is one ref a push updates from the object old to new.
type command struct {
	old, new, ref string
}

// push is the start of a push: its commands, the capabilities of the
// client, and the bytes they came in, which go on to the server as they
// are.
type push struct {
	commands     []command
	capabilities []string
	raw          []byte
}

var commandLine = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64}) ([0-9a-f]{40}|[0-9a-f]{64}) (refs/[^\x00-\x20\x7f]+)$`)

// readPush reads the commands of a push up to the flush that ends them.
func readPush(r *bufio.Reader) (push, error) {
	var p push

	for {
		line, raw, err := readPktLine(r)
		if err != nil {
			return push{}, err
		}

		p.raw = append(p.raw, raw...)

		if line == nil {
			break
		}

		if len(p.commands) == maxCommands {
			return push{}, errTooManyRefs
		}

		text := strings.TrimSuffix(string(line), "\n")

		if len(p.commands) == 0 {
			switch {
			case strings.HasPrefix(text, "shallow "):
				return push{}, errShallow
			case strings.HasPrefix(text, "push-cert"):
				return push{}, errPushCert
			}

			var capabilities string

			text, capabilities, _ = strings.Cut(text, "\x00")
			p.capabilities = strings.Fields(capabilities)

			if slices.Contains(p.capabilities, "push-options") {
				return push{}, errPushOptions
			}
		}

		match := commandLine.FindStringSubmatch(text)
		if match == nil {
			return push{}, errBadPktLine
		}

		if len(match[1]) != len(match[2]) {
			return push{}, errMixedObjects
		}

		p.commands = append(p.commands, command{old: match[1], new: match[2], ref: match[3]})
	}

	if len(p.commands) == 0 {
		return push{}, errNoCommands
	}

	return p, nil
}

// readPktLine returns the content of the next pkt-line and its bytes, or
// no content for a flush.
func readPktLine(r *bufio.Reader) (line, raw []byte, err error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, nil, errBadPktLine
	}

	if string(header) == flush {
		return nil, header, nil
	}

	length, err := strconv.ParseUint(string(header), 16, 16)
	if err != nil || length <= 4 || length > maxPktLine || strings.ToLower(string(header)) != string(header) {
		return nil, nil, errBadPktLine
	}

	raw = make([]byte, length)
	copy(raw, header)

	if _, err := io.ReadFull(r, raw[4:]); err != nil {
		return nil, nil, errBadPktLine
	}

	return raw[4:], raw, nil
}

// pktLine returns text as a pkt-line.
func pktLine(text string) string {
	return fmt.Sprintf("%04x%s", len(text)+4, text)
}

// refusal is a ref of a push and why aibox refused it.
type refusal struct {
	ref, reason string
}

// writeReport answers a push as the server would after refusing each ref
// for its reason, so that git in the VM shows the reasons. With a side
// band the report goes in band 1 of it.
func writeReport(w http.ResponseWriter, capabilities []string, refusals []refusal) {
	var report strings.Builder

	report.WriteString(pktLine("unpack ok\n"))

	for _, refusal := range refusals {
		report.WriteString(pktLine("ng " + refusal.ref + " aibox: " + refusal.reason + "\n"))
	}

	report.WriteString(flush)

	body := report.String()

	band := 0

	switch {
	case slices.Contains(capabilities, "side-band-64k"):
		band = maxPktLine - 5
	case slices.Contains(capabilities, "side-band"):
		band = 1000 - 5
	}

	if band > 0 {
		var banded bytes.Buffer

		for chunk := range slices.Chunk([]byte(body), band) {
			banded.WriteString(pktLine("\x01" + string(chunk)))
		}

		banded.WriteString(flush)
		body = banded.String()
	}

	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}
