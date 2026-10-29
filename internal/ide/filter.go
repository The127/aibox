package ide

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/the127/aibox/internal/realpath"
)

// handshake are the methods of the VM that set up the connection and do
// nothing in the IDE.
var handshake = map[string]bool{
	"initialize":                true,
	"notifications/initialized": true,
	"ide_connected":             true,
	"tools/list":                true,
}

// tool is a tool of the IDE that Claude Code in the VM may call: the
// arguments that go on to the IDE, and what makes them fit for the host.
type tool struct {
	args []string
	fit  func(*filter, message) bool
}

// tools are the tools of the IDE that Claude Code in the VM may call. They
// show the IDE something or read what it reports, and only of files in the
// project. executeCode runs code on the host and stays out, like every
// tool not named here.
var tools = map[string]tool{
	"openDiff": {
		args: []string{"old_file_path", "new_file_path", "new_file_contents", "tab_name"},
		fit: func(f *filter, args message) bool {
			if !pathTo(args, "old_file_path", f.paths.toHost) || !pathTo(args, "new_file_path", f.paths.toHost) || len(args.str("tab_name")) > maxNameBytes {
				return false
			}

			f.tabs.put(args.str("tab_name"), true)

			return true
		},
	},
	"close_tab": {
		args: []string{"tab_name"},
		// only a tab the VM opened, not any tab of the person
		fit: func(f *filter, args message) bool {
			name := args.str("tab_name")
			if _, opened := f.tabs.take(name); !opened {
				return false
			}

			return true
		},
	},
	"closeAllDiffTabs": {fit: func(f *filter, _ message) bool {
		f.tabs.clear()

		return true
	}},
	"getDiagnostics": {
		args: []string{"uri"},
		fit: func(f *filter, args message) bool {
			_, has := args["uri"]

			return !has || pathTo(args, "uri", f.paths.uriToHost)
		},
	},
}

// pathTo translates the path under the key. A path that has no
// translation, or a value that is no string, makes the message unfit.
func pathTo(m message, key string, translate func(string) (string, bool)) bool {
	var name string
	if json.Unmarshal(m[key], &name) != nil {
		return false
	}

	translated, ok := translate(name)
	if !ok {
		return false
	}

	m[key], _ = json.Marshal(translated)

	return true
}

var (
	errNotPassed = errors.New("aibox does not pass this on to the IDE")
	errOutside   = errors.New("aibox passes on only paths inside /project and tabs the VM opened")
	errNoJSONRPC = errors.New("a message that is no JSON-RPC object")
	errLongID    = errors.New("a request with an ID too long to keep")
)

// maxNameBytes is how long an ID or a tab name of the VM may be. The
// filter keeps them until the IDE answers or the tab closes, and Claude
// Code's are short.
const maxNameBytes = 1 << 10

// request is a request of the VM that the filter passed on: the ID the VM
// gave it and what it asked for.
type request struct {
	id   json.RawMessage
	kind string
}

// filter decides what goes between the VM and the IDE on one link. It
// gives each request it passes on an ID of its own and remembers it, so
// that it knows what an answer answers. An ID of the VM's would let the VM
// give two requests the same ID and pass an answer off as another one.
type filter struct {
	paths paths
	host  *regexp.Regexp

	mu      sync.Mutex
	next    int
	pending *kept[request]
	// tabs are the diff tabs the VM opened
	tabs *kept[bool]
}

func newFilter(p paths) *filter {
	return &filter{paths: p, host: hostPattern(p.host), pending: newKept[request](), tabs: newKept[bool]()}
}

// toIDE decides about a message of the VM: what goes on to the IDE, or
// what goes back to the VM in its place. A request the filter holds back
// gets an error, so that Claude Code does not wait for an answer. A
// message that is no JSON object ends the link, since Claude Code sends
// none.
func (f *filter) toIDE(data []byte) (forward, reply []byte, err error) {
	m, ok := parse(data)
	if !ok {
		return nil, nil, errNoJSONRPC
	}

	method, isRequest := m.method()
	if !isRequest {
		return nil, nil, nil
	}

	id, hasID := m["id"]
	if len(id) > maxNameBytes {
		return nil, nil, errLongID
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	kind, why := method, errNotPassed

	switch {
	case handshake[method]:
		why = nil
		m = message{"jsonrpc": m["jsonrpc"], "method": m["method"], "params": m["params"]}
	case method == "tools/call" && hasID:
		var params, args message
		_ = json.Unmarshal(m["params"], &params)
		_ = json.Unmarshal(params["arguments"], &args)

		name := params.str("name")
		kind = "tools/call " + name

		if t, known := tools[name]; known {
			// only the arguments the filter knows, so that no other path
			// slips by
			allowed := message{}
			for _, key := range t.args {
				if value, has := args[key]; has {
					allowed[key] = value
				}
			}

			if why = errOutside; t.fit(f, allowed) {
				why = nil
				encoded, _ := json.Marshal(allowed)
				m = message{"jsonrpc": m["jsonrpc"], "method": m["method"]}
				m["params"], _ = json.Marshal(message{"name": params["name"], "arguments": encoded})
			}
		}
	}

	if why != nil {
		if !hasID {
			return nil, nil, nil
		}

		reply, err = json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": -32601, "message": why.Error()},
		})

		return nil, reply, err
	}

	if m["params"] == nil {
		delete(m, "params")
	}

	if hasID {
		f.next++
		own := strconv.Itoa(f.next)
		f.pending.put(own, request{id: id, kind: kind})
		m["id"] = json.RawMessage(own)
	}

	forward, err = json.Marshal(m)

	return forward, nil, err
}

// toVM decides about a message of the IDE: what goes on to the VM, or what
// goes back to the IDE in its place. Answers to the requests the filter
// passed on go to the VM, fit for it, and so do the notifications that
// tell Claude Code what the person does in the project. The filter answers
// a ping of the IDE itself, so that the IDE keeps the link, without
// letting the IDE ask the VM anything.
func (f *filter) toVM(data []byte) (forward, reply []byte) {
	m, ok := parse(data)
	if !ok {
		return nil, nil
	}

	if method, isRequest := m.method(); isRequest {
		id, hasID := m["id"]
		if hasID && method == "ping" {
			reply, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})

			return nil, reply
		}

		if hasID || !f.notification(method, m) {
			return nil, nil
		}
	} else if !f.answer(m) {
		return nil, nil
	}

	forward, err := json.Marshal(m)
	if err != nil {
		return nil, nil
	}

	return forward, nil
}

// answer makes an answer of the IDE fit for the VM, with the ID the VM
// gave the request, or tells that it answers no request the filter passed
// on.
func (f *filter) answer(m message) bool {
	id, hasID := m["id"]
	if !hasID {
		return false
	}

	f.mu.Lock()
	r, waiting := f.pending.take(string(id))
	f.mu.Unlock()

	if !waiting {
		return false
	}

	m["id"] = r.id

	if failure, has := m["error"]; has {
		// an error may name the project by its path on the host
		m["error"] = f.hidden(failure)

		return true
	}

	result, ok := parse(m["result"])
	if !ok {
		m["result"] = f.hidden(m["result"])

		return true
	}

	switch r.kind {
	case "tools/list":
		var list []message
		_ = json.Unmarshal(result["tools"], &list)

		allowed := []message{}

		for _, t := range list {
			if _, known := tools[t.str("name")]; known {
				allowed = append(allowed, t)
			}
		}

		result["tools"], _ = json.Marshal(allowed)
	case "tools/call getDiagnostics":
		f.diagnostics(result)
	default:
		m["result"] = f.hidden(m["result"])

		return true
	}

	m["result"], _ = json.Marshal(result)

	return true
}

// hidden is the JSON with the path of the project on the host turned into
// /project wherever it shows up, as it is written in JSON.
func (f *filter) hidden(raw json.RawMessage) json.RawMessage {
	if f.host == nil || len(raw) == 0 {
		return raw
	}

	return f.host.ReplaceAll(raw, []byte(vmProject+"$1"))
}

// hostPattern finds the path of the project on the host in JSON, the way
// Go writes it and the way an IDE may, with & < > as they are, and also
// the path its symlinks lead to. Only the whole name of the folder counts,
// so that a folder next to it like aibox-old stays as it is.
func hostPattern(host string) *regexp.Regexp {
	if host == "" {
		return nil
	}

	names := []string{filepath.Clean(host)}
	if resolved, err := realpath.Of(host); err == nil && resolved != names[0] {
		names = append(names, resolved)
	}

	var needles []string

	for _, name := range names {
		for _, escapeHTML := range []bool{true, false} {
			var b bytes.Buffer

			encoder := json.NewEncoder(&b)
			encoder.SetEscapeHTML(escapeHTML)
			_ = encoder.Encode(name)

			quoted := strings.TrimSpace(b.String())
			needles = append(needles, regexp.QuoteMeta(quoted[1:len(quoted)-1]))
		}
	}

	// the longest first, since the first that matches wins
	slices.SortFunc(needles, func(a, b string) int { return len(b) - len(a) })

	return regexp.MustCompile(`(?:` + strings.Join(needles, "|") + `)([/"\\]|$)`)
}

// diagnostics keeps of the diagnostics the IDE reports, a JSON list in a
// text, those of files in the project, with the URIs of the VM.
func (f *filter) diagnostics(result message) {
	var content []message
	_ = json.Unmarshal(result["content"], &content)

	var isError bool
	_ = json.Unmarshal(result["isError"], &isError)

	if isError || content == nil {
		if raw, has := result["content"]; has {
			result["content"] = f.hidden(raw)
		}

		return
	}

	for _, part := range content {
		var files []message
		if json.Unmarshal([]byte(part.str("text")), &files) != nil {
			if raw, has := part["text"]; has {
				part["text"] = f.hidden(raw)
			}

			continue
		}

		kept := []message{}

		for _, file := range files {
			if pathTo(file, "uri", f.paths.uriToVM) {
				kept = append(kept, file)
			}
		}

		text, _ := json.Marshal(kept)
		part["text"], _ = json.Marshal(string(f.hidden(text)))
	}

	result["content"], _ = json.Marshal(content)
}

// notification makes a notification of the IDE fit for the VM, or tells
// that it does not go there. The selection and what the person mentions
// go only when they are in the project, since they carry the text of the
// file.
func (f *filter) notification(method string, m message) bool {
	params, ok := parse(m["params"])
	if !ok {
		return false
	}

	switch method {
	case "selection_changed", "at_mentioned":
		if !pathTo(params, "filePath", f.paths.toVM) {
			return false
		}

		if _, has := params["fileUrl"]; has && !pathTo(params, "fileUrl", f.paths.uriToVM) {
			return false
		}
	case "diagnostics_changed":
		var uris []string
		_ = json.Unmarshal(params["uris"], &uris)

		kept := []string{}

		for _, uri := range uris {
			if translated, ok := f.paths.uriToVM(uri); ok {
				kept = append(kept, translated)
			}
		}

		if len(kept) == 0 {
			return false
		}

		params["uris"], _ = json.Marshal(kept)
	default:
		return false
	}

	m["params"], _ = json.Marshal(params)

	return true
}
