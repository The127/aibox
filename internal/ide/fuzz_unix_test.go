//go:build !windows

package ide

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// fuzzProject is a project folder on the host with a file, a folder, a
// symlink that leads out of it and one that leads nowhere, and the paths
// of the project as the filter of a link to it sees them.
func fuzzProject(f *testing.F) (string, paths) {
	f.Helper()

	host, err := filepath.EvalSymlinks(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}

	outside := f.TempDir()

	for _, err := range []error{
		os.WriteFile(filepath.Join(host, "main.go"), nil, 0o600),
		os.Mkdir(filepath.Join(host, "sub"), 0o750),
		os.Symlink(outside, filepath.Join(host, "out")),
		os.Symlink(filepath.Join(outside, "missing"), filepath.Join(host, "dangling")),
	} {
		if err != nil {
			f.Fatal(err)
		}
	}

	return host, paths{host: host}
}

// staysInside tells, without the code under test, whether the host path
// leads into the project once symlinks are followed: the first part of it
// that exists has to resolve into the project, and nothing below that part
// may exist as a dangling link.
func staysInside(project, host string) bool {
	for name := host; ; name = filepath.Dir(name) {
		resolved, err := filepath.EvalSymlinks(name)
		if err == nil {
			rest, err := filepath.Rel(project, resolved)

			return err == nil && rest != ".." && !strings.HasPrefix(rest, "../")
		}

		if _, err := os.Lstat(name); err == nil || name == filepath.Dir(name) {
			return false
		}
	}
}

// FuzzToHost translates paths from the VM to the host. A path toHost gives
// leads into the project, also through symlinks the VM put there.
func FuzzToHost(f *testing.F) {
	project, p := fuzzProject(f)

	for _, seed := range []string{"/project/main.go", "/project/sub/new.go", "/project/out/secret", "/project/dangling", "/project/../etc/passwd", "/projectx/a", "project/main.go", "/project//sub/./x"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, vm string) {
		host, ok := p.toHost(vm)
		if ok && !staysInside(project, host) {
			t.Fatalf("%q became %q, outside the project", vm, host)
		}

		uri, ok := p.uriToHost("file://" + vm)
		if !ok {
			return
		}

		u, isFile := fileURI(uri)
		if !isFile || !staysInside(project, u.Path) {
			t.Fatalf("the URI of %q became %q, outside the project", vm, uri)
		}
	})
}

// FuzzToVM translates paths from the IDE to the VM. A path toVM gives is
// in /project, and its file on the host is inside the project.
func FuzzToVM(f *testing.F) {
	project, p := fuzzProject(f)

	for _, seed := range []string{"main.go", "sub/x", "out/secret", "dangling", "../other", ""} {
		f.Add(filepath.Join(project, seed))
	}

	f.Add("/etc/passwd")

	f.Fuzz(func(t *testing.T, host string) {
		vm, ok := p.toVM(host)
		if !ok {
			return
		}

		if vm != vmProject && !strings.HasPrefix(vm, vmProject+"/") || !staysInside(project, host) {
			t.Fatalf("%q became %q", host, vm)
		}
	})
}

// FuzzFilterToIDE passes messages from the VM to the IDE. What goes on is
// a handshake or a call of a tool the filter allows, with only the
// arguments the filter knows and only paths inside the project, and never
// executeCode, which runs code on the host.
func FuzzFilterToIDE(f *testing.F) {
	project, p := fuzzProject(f)

	f.Add([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"openDiff","arguments":{"old_file_path":"/project/main.go","new_file_path":"/project/main.go","new_file_contents":"x","tab_name":"t","extra":"/etc/passwd"}}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{"uri":"file:///project/out/secret"}}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"executeCode","arguments":{"code":"1"}}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","id":5,"method":"initialize","params":{},"extra":1}`))
	f.Add([]byte(`[1,2]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		forward, _, _ := newFilter(p).toIDE(data)
		if forward == nil {
			return
		}

		var m struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}

		if err := json.Unmarshal(forward, &m); err != nil {
			t.Fatalf("passed on %q, which is no JSON-RPC", forward)
		}

		if handshake[m.Method] {
			return
		}

		var call struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}

		_ = json.Unmarshal(m.Params, &call)

		tool, known := tools[call.Name]
		if m.Method != "tools/call" || !known || call.Name == "executeCode" {
			t.Fatalf("passed on %q", forward)
		}

		for key, raw := range call.Arguments {
			if !slices.Contains(tool.args, key) {
				t.Fatalf("passed on the argument %q", key)
			}

			var value string
			_ = json.Unmarshal(raw, &value)

			switch key {
			case "old_file_path", "new_file_path":
				if !staysInside(project, value) {
					t.Fatalf("passed on the path %q", value)
				}
			case "uri":
				if u, ok := fileURI(value); !ok || !staysInside(project, u.Path) {
					t.Fatalf("passed on the URI %q", value)
				}
			}
		}
	})
}

// stringsIn calls found with every string and key of the JSON value, at
// any depth.
func stringsIn(value any, found func(string)) {
	switch v := value.(type) {
	case string:
		found(v)
	case []any:
		for _, item := range v {
			stringsIn(item, found)
		}
	case map[string]any:
		for key, item := range v {
			found(key)
			stringsIn(item, found)
		}
	}
}

// FuzzFilterToVM passes messages from the IDE to the VM. Nothing that goes
// on names the project by its path on the host, which holds the person's
// user name, however the IDE wrote it in JSON.
func FuzzFilterToVM(f *testing.F) {
	project, p := fuzzProject(f)
	quotedProject, _ := json.Marshal(project + "/main.go")

	// the rule the filter promises: the folder by its whole name, so that
	// a folder next to it like project-old doesn't count
	namesProject := regexp.MustCompile(regexp.QuoteMeta(project) + `([/\\]|\z)`)

	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":` + string(quotedProject) + `}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","method":"selection_changed","params":{"text":"x","filePath":` + string(quotedProject) + `}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	f.Add([]byte(`{"jsonrpc":"2.0","method":"selection_changed","params":{"text":"x","filePath":"` + strings.ReplaceAll(project, "/", `\u002f`) + `\u002fmain.go"}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"` + strings.ReplaceAll(project, "/", `\u002f`) + `\u002fmain.go"}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		filter := newFilter(p)
		_, _, _ = filter.toIDE([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{}}}`))

		forward, _ := filter.toVM(data)
		if forward == nil {
			return
		}

		var value any
		if err := json.Unmarshal(forward, &value); err != nil {
			t.Fatalf("passed on %q, which is no JSON", forward)
		}

		stringsIn(value, func(s string) {
			if namesProject.MatchString(s) {
				t.Fatalf("passed on %q, which names the project on the host", s)
			}
		})
	})
}

// FuzzSummary describes messages for the log. Nothing a terminal acts on
// comes out, and a name is cut to maxLoggedName.
func FuzzSummary(f *testing.F) {
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"\u001b[2J"}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","method":"x\u202ey"}`))
	f.Add([]byte(`no json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		text := summary(data)

		for _, r := range text {
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
				t.Fatalf("the summary %q of %q has %U", text, data, r)
			}
		}

		// strconv.Quote writes a byte as at most ten, as \U0010ffff
		if len(text) > len("tools/call ")+len(`""`)+10*maxLoggedName+len("...") {
			t.Fatalf("the summary of %q is %d bytes", data, len(text))
		}
	})
}
