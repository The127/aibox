package task_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/the127/aibox/internal/task"
)

var objectID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

const (
	fuzzBase = "1111111111111111111111111111111111111111"
	fuzzHead = "2222222222222222222222222222222222222222"
)

// FuzzReadBundle reads bundle headers the VM could send. A header ReadBundle
// takes names only commits, and one CheckClone takes needs only the base and
// carries only the branches of a clone.
func FuzzReadBundle(f *testing.F) {
	f.Add([]byte("# v2 git bundle\n-" + fuzzBase + " one\n" + fuzzHead + " " + task.CloneBranch + "\n\nPACK"))
	f.Add([]byte("# v3 git bundle\n@object-format=sha1\n" + fuzzHead + " " + task.CloneBranch + "\n" + fuzzBase + " " + task.CloneEndBranch + "\n\n"))
	f.Add([]byte("# v2 git bundle\n" + fuzzHead + " refs/heads/main\n\n"))
	f.Add([]byte("# v2 git bundle\n-zz\n\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		bundle, err := task.ReadBundle(bytes.NewReader(data))
		if err != nil {
			return
		}

		for _, prerequisite := range bundle.Prerequisites {
			if !objectID.MatchString(prerequisite) {
				t.Fatalf("prerequisite %q is no commit", prerequisite)
			}
		}

		for ref, commit := range bundle.Refs {
			if ref == "" || !objectID.MatchString(commit) {
				t.Fatalf("ref %q at %q", ref, commit)
			}
		}

		heads, err := bundle.CheckClone(fuzzBase, task.CloneBranch, task.CloneEndBranch)
		if err != nil {
			return
		}

		if _, ok := heads[task.CloneBranch]; !ok {
			t.Fatalf("CheckClone took a bundle without %s", task.CloneBranch)
		}

		for ref := range heads {
			if ref != task.CloneBranch && ref != task.CloneEndBranch {
				t.Fatalf("CheckClone took %q", ref)
			}
		}

		for _, prerequisite := range bundle.Prerequisites {
			if prerequisite != fuzzBase {
				t.Fatalf("CheckClone took a bundle that needs %s", prerequisite)
			}
		}
	})
}

// seedArchive is a tar archive of the files, in that order.
func seedArchive(f *testing.F, files ...[2]string) []byte {
	f.Helper()

	var out bytes.Buffer

	w := tar.NewWriter(&out)

	for _, file := range files {
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: file[0], Mode: 0o644, Size: int64(len(file[1]))}); err != nil {
			f.Fatal(err)
		}

		if _, err := w.Write([]byte(file[1])); err != nil {
			f.Fatal(err)
		}
	}

	if err := w.Close(); err != nil {
		f.Fatal(err)
	}

	return out.Bytes()
}

// FuzzReceive reads archives of results the VM could send. What Receive
// takes ends with result.json, and no file is larger than its limit.
func FuzzReceive(f *testing.F) {
	f.Add(seedArchive(f, [2]string{task.ChangesFile, "# v2 git bundle\n"}, [2]string{task.ResultFile, "{}"}))
	f.Add(seedArchive(f, [2]string{task.ResultFile, `{"error":"x"}`}))
	f.Add(seedArchive(f, [2]string{"../escape", "x"}, [2]string{task.ResultFile, "{}"}))
	f.Add([]byte("not an archive"))

	f.Add(seedArchive(f, [2]string{task.ResultFile, "{}"}, [2]string{task.ChangesFile, "late"}))
	f.Add(seedArchive(f, [2]string{task.ChangesFile, "a"}, [2]string{task.ChangesFile, "b"}, [2]string{task.ResultFile, "{}"}))

	f.Fuzz(func(t *testing.T, data []byte) {
		var written []string

		changes := &recorder{name: task.ChangesFile, written: &written}
		result := &recorder{name: task.ResultFile, written: &written}

		err := task.Receive(bytes.NewReader(data), map[string]io.Writer{task.ChangesFile: changes, task.ResultFile: result})
		if err != nil {
			return
		}

		if changes.n > task.MaxChangesBytes || result.n > task.MaxResultBytes {
			t.Fatalf("Receive took %d and %d bytes", changes.n, result.n)
		}

		if i := slices.Index(written, task.ResultFile); i >= 0 && slices.Contains(written[i:], task.ChangesFile) {
			t.Fatalf("Receive took %s after %s", task.ChangesFile, task.ResultFile)
		}
	})
}

// recorder counts what is written to it and notes its name for each write.
type recorder struct {
	name    string
	n       int
	written *[]string
}

func (r *recorder) Write(b []byte) (int, error) {
	r.n += len(b)
	*r.written = append(*r.written, r.name)

	return len(b), nil
}

// safeForTerminal fails the test on a character that a terminal acts on.
func safeForTerminal(t *testing.T, text string, newlines bool) {
	t.Helper()

	if !utf8.ValidString(text) {
		t.Fatalf("%q is no UTF-8", text)
	}

	for _, r := range text {
		if r == '\n' && newlines {
			continue
		}

		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			t.Fatalf("%q has %U", text, r)
		}
	}
}

// FuzzCleanText cleans text the VM could send to the terminal. No control
// or format character comes out, and CleanLine keeps to one line.
func FuzzCleanText(f *testing.F) {
	f.Add("ok\x1b]52;c;aGk=\x07\tdone\u202eevil\u200b\u0085\xff\r\nnext")
	f.Add("a\u2028b\u2029c")
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		safeForTerminal(t, task.CleanText(text), true)
		safeForTerminal(t, task.CleanLine(text), false)

		for line := range strings.SplitSeq(task.Indent(text, "| "), "\n") {
			if !strings.HasPrefix(line, "| ") {
				t.Fatalf("line %q of Indent has no prefix", line)
			}
		}
	})
}

// scpAddress is an address of SSH as user@host:path.
var scpAddress = regexp.MustCompile(`^([^@:/]+)@([^@:/]+):(.*)$`)

var stamped = regexp.MustCompile(`^12:34:56 vm\[a1b2c3\]: `)

// FuzzLogWriter writes what the VM could print through a log of a task.
// Every line that comes out starts with the stamp, so the VM can't pass a
// line off as one of aibox, and carries nothing a terminal acts on.
func FuzzLogWriter(f *testing.F) {
	f.Add([]byte("progress\naibox[a1b2c3]: done\n"), 40)
	f.Add([]byte("vm: warning\r\n\x1b[2Jlong line without an end"), 0)
	f.Add([]byte("日本語の行がとても長く続きます"), 23)

	f.Fuzz(func(t *testing.T, data []byte, width int) {
		var out bytes.Buffer

		log := task.NewLog(&out, "a1b2c3", func() time.Time { return time.Date(2026, 10, 29, 12, 34, 56, 0, time.UTC) })
		log.Wrap(func() int { return width % 200 })

		w := log.Writer("vm")
		_, _ = w.Write(data)
		_ = w.Close()

		for line := range strings.SplitSeq(strings.TrimSuffix(out.String(), "\n"), "\n") {
			if out.Len() == 0 {
				break
			}

			if !stamped.MatchString(line) {
				t.Fatalf("line %q has no stamp", line)
			}

			safeForTerminal(t, line, false)

			// a character other than ASCII counts as two columns, as the
			// log counts it, and the last column of the terminal stays free
			if columns := width % 200; columns > 0 {
				stamp := stamped.FindString(line)
				if used := len(stamp) + displayWidth(line[len(stamp):]); used > max(columns-1, len(stamp)+8) {
					t.Fatalf("line %q takes %d columns of %d", line, used, columns)
				}
			}
		}
	})
}

// displayWidth is how many columns the log counts for the text.
func displayWidth(text string) int {
	width := 0

	for _, r := range text {
		width++
		if r >= utf8.RuneSelf {
			width++
		}
	}

	return width
}

// FuzzCheckRemote checks remotes the host could hand a clone. One it takes
// has a name git keeps under refs/remotes and an address of HTTPS or SSH
// with no login, query, fragment or leading dash.
func FuzzCheckRemote(f *testing.F) {
	f.Add("origin", "https://github.com/owner/repo.git")
	f.Add("fork", "git@github.com:me/repo.git")
	f.Add("ssh", "ssh://git@github.com:22/o/r")
	f.Add("token", "https://x:secret@github.com/o/r")
	f.Add("-o", "ssh://-oProxyCommand=x/p")
	f.Add("plain", "http://gitea.local:3000/o/r")
	f.Add("scp", "-x@host:path")
	f.Add("scp", "git@host:-path")

	f.Fuzz(func(t *testing.T, name, address string) {
		if !task.CheckRemote(task.Remote{Name: name, URL: address}) {
			return
		}

		switch {
		case name == "" || name[0] == '-' || name[0] == '.' || strings.ContainsAny(name, "/ \\"):
			t.Fatalf("took the name %q", name)
		case address == "" || address[0] == '-' || strings.ContainsAny(address, "?# \t\r\n"):
			t.Fatalf("took the address %q", address)
		}

		if scp := scpAddress.FindStringSubmatch(address); scp != nil {
			if scp[1][0] == '-' || scp[2][0] == '-' || strings.HasPrefix(scp[3], "-") {
				t.Fatalf("took the option %q", address)
			}

			return
		}

		if !strings.HasPrefix(address, "https://") && !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "ssh://") {
			t.Fatalf("took %q, which is no address of HTTPS or SSH", address)
		}

		u, err := url.Parse(address)
		if err != nil {
			return
		}

		if _, hasPassword := u.User.Password(); hasPassword || strings.HasPrefix(address, "http") && u.User != nil {
			t.Fatalf("took the login of %q", address)
		}

		if strings.HasPrefix(u.Hostname(), "-") || strings.HasPrefix(u.User.Username(), "-") {
			t.Fatalf("took the option %q", address)
		}
	})
}

// FuzzCloneCheck checks clone.json as the VM reads it. A clone Check takes
// starts from a commit and has only remotes CheckRemote takes.
func FuzzCloneCheck(f *testing.F) {
	f.Add([]byte(`{"base":"` + fuzzBase + `","remotes":[{"name":"origin","url":"https://github.com/o/r"}]}`))
	f.Add([]byte(`{"base":"HEAD"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var clone task.Clone
		if json.Unmarshal(data, &clone) != nil || clone.Check() != nil {
			return
		}

		if !objectID.MatchString(clone.Base) {
			t.Fatalf("took the base %q", clone.Base)
		}

		for _, remote := range clone.Remotes {
			if !task.CheckRemote(remote) {
				t.Fatalf("took the remote %+v", remote)
			}
		}
	})
}

// FuzzSettingsCheck checks task.json as the VM reads it. Settings Check
// takes hold no model git or Claude Code could read as an option, and a
// git identity without control characters or angle brackets.
func FuzzSettingsCheck(f *testing.F) {
	f.Add([]byte(`{"model":"opus","maxTurns":3,"gitName":"Some One","gitEmail":"a@b.c"}`))
	f.Add([]byte(`{"model":"--help"}`))
	f.Add([]byte("{\"model\":\"a\u00a0b\",\"gitName\":\"a\\u0085b\"}"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var settings task.Settings
		if json.Unmarshal(data, &settings) != nil || settings.Check() != nil {
			return
		}

		switch {
		case strings.HasPrefix(settings.Model, "-") || strings.ContainsFunc(settings.Model, func(r rune) bool { return r <= ' ' || r == 0x7f }):
			t.Fatalf("took the model %q", settings.Model)
		case strings.ContainsAny(settings.GitName+settings.GitEmail, "<>\n\r\x00"):
			t.Fatalf("took the identity %q <%q>", settings.GitName, settings.GitEmail)
		case settings.MaxTurns < 0 || settings.MaxBudgetUSD < 0 || settings.TimeoutSeconds < 0:
			t.Fatalf("took a negative limit %+v", settings)
		}
	})
}
