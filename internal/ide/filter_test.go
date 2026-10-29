package ide

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/testlink"
)

// project is a project folder on the host with a file, as the filter of a
// link to it sees it.
func project(t *testing.T) (string, *filter) {
	t.Helper()

	host, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(host, "main.go"), nil, 0o600))

	return host, newFilter(paths{host: host})
}

const outside = `{"jsonrpc":"2.0","id":5,"error":{"code":-32601,"message":"aibox passes on only paths inside /project and tabs the VM opened"}}`

func TestFilterPassesAnAllowedToolWithThePathsOfTheHost(t *testing.T) {
	// arrange
	host, f := project(t)
	file := jsonString(filepath.Join(host, "main.go"))

	tests := map[string]struct{ call, want string }{
		"openDiff": {
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"openDiff","arguments":{"old_file_path":"/project/main.go","new_file_path":"/project/main.go","new_file_contents":"package main\n","tab_name":"t"}}}`,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"openDiff","arguments":{"old_file_path":` + file + `,"new_file_path":` + file + `,"new_file_contents":"package main\n","tab_name":"t"}}}`,
		},
		"getDiagnostics of a file": {
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{"uri":"file:///project/main.go"}}}`,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{"uri":"` + uriOf(filepath.Join(host, "main.go")) + `"}}}`,
		},
		"getDiagnostics of all": {
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{}}}`,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{}}}`,
		},
		"closeAllDiffTabs": {
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"closeAllDiffTabs","arguments":{}}}`,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"closeAllDiffTabs","arguments":{}}}`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFilter(f.paths)

			// act
			forward, reply, err := f.toIDE([]byte(test.call))

			// assert
			require.NoError(t, err)
			assert.Nil(t, reply)
			assert.JSONEq(t, test.want, string(forward))
		})
	}
}

func TestFilterRefusesPathsOutsideTheProject(t *testing.T) {
	// arrange
	host, f := project(t)
	testlink.Folder(t, filepath.Dir(host), filepath.Join(host, "out"))

	for name, args := range map[string]string{
		"a path above":                 `"old_file_path":"/project/../etc/passwd","new_file_path":"/project/main.go"`,
		"another folder":               `"old_file_path":"/home/user/.bashrc","new_file_path":"/project/main.go"`,
		"a relative path":              `"old_file_path":"main.go","new_file_path":"/project/main.go"`,
		"a symlink out of the project": `"old_file_path":"/project/main.go","new_file_path":"/project/out/x"`,
		"no path":                      `"new_file_path":"/project/main.go"`,
		"a path that is no string":     `"old_file_path":7,"new_file_path":"/project/main.go"`,
	} {
		t.Run(name, func(t *testing.T) {
			// act
			forward, reply, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"openDiff","arguments":{` + args + `}}}`))

			// assert
			require.NoError(t, err)
			assert.Nil(t, forward)
			assert.JSONEq(t, outside, string(reply))
		})
	}
}

func TestFilterRefusesADiagnosticsURIOutsideTheProject(t *testing.T) {
	// arrange
	_, f := project(t)

	for _, uri := range []string{"file:///etc/passwd", "git:/project/main.go", "file://server/project/main.go"} {
		t.Run(uri, func(t *testing.T) {
			// act
			forward, reply, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{"uri":"` + uri + `"}}}`))

			// assert
			require.NoError(t, err)
			assert.Nil(t, forward)
			assert.JSONEq(t, outside, string(reply))
		})
	}
}

func TestFilterRefusesToolsItDoesNotAllow(t *testing.T) {
	// arrange
	_, f := project(t)

	for _, name := range []string{"executeCode", "openFile", "saveDocument", ""} {
		t.Run(name, func(t *testing.T) {
			// act
			forward, reply, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`))

			// assert
			require.NoError(t, err)
			assert.Nil(t, forward)
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"error":{"code":-32601,"message":"aibox does not pass this on to the IDE"}}`, string(reply))
		})
	}
}

func TestFilterGivesTheVMTheDiagnosticsOfTheProjectOnly(t *testing.T) {
	// arrange
	host, f := project(t)
	_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{}}}`))
	require.NoError(t, err)

	inside := uriOf(filepath.Join(host, "main.go"))
	text := `[{"uri":"` + inside + `","diagnostics":[{"message":"unused"}]},{"uri":"file:///Users/someone/secret/x.go","diagnostics":[{"message":"secret"}]}]`
	answer, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
	require.NoError(t, err)

	// act
	forward, _ := f.toVM(answer)

	// assert
	want := `[{"diagnostics":[{"message":"unused"}],"uri":"file:///project/main.go"}]`
	wantAnswer, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 5, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": want}}}})
	require.NoError(t, err)
	assert.JSONEq(t, string(wantAnswer), string(forward))
}

func TestFilterPassesTheAnswerToARequestOnlyOnce(t *testing.T) {
	// arrange
	_, f := project(t)
	_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"closeAllDiffTabs","arguments":{}}}`))
	require.NoError(t, err)

	answer := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"CLOSED_0_DIFF_TABS"}]}}`)

	// act
	first, _ := f.toVM(answer)
	second, _ := f.toVM(answer)

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"content":[{"type":"text","text":"CLOSED_0_DIFF_TABS"}]}}`, string(first))
	assert.Nil(t, second)
}

func TestFilterGivesTheVMWhatThePersonSelectsInTheProject(t *testing.T) {
	// arrange
	host, f := project(t)
	file := filepath.Join(host, "main.go")
	selection, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "selection_changed", "params": map[string]any{
		"text": "package main", "filePath": file, "fileUrl": uriOf(file),
		"selection": map[string]any{"start": map[string]any{"line": 0, "character": 0}},
	}})
	require.NoError(t, err)

	// act
	forward, _ := f.toVM(selection)

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","method":"selection_changed","params":{"text":"package main","filePath":"/project/main.go","fileUrl":"file:///project/main.go","selection":{"start":{"line":0,"character":0}}}}`, string(forward))
}

func TestFilterKeepsFromTheVMWhatThePersonDoesOutsideTheProject(t *testing.T) {
	// arrange
	_, f := project(t)

	for name, message := range map[string]string{
		"a selection elsewhere": `{"jsonrpc":"2.0","method":"selection_changed","params":{"text":"-----BEGIN OPENSSH PRIVATE KEY-----","filePath":"/Users/someone/.ssh/id_ed25519"}}`,
		"a mention elsewhere":   `{"jsonrpc":"2.0","method":"at_mentioned","params":{"filePath":"/Users/someone/notes.txt"}}`,
		"diagnostics elsewhere": `{"jsonrpc":"2.0","method":"diagnostics_changed","params":{"uris":["git:/Users/someone/repo/justfile.git"]}}`,
		"another notification":  `{"jsonrpc":"2.0","method":"some/notification","params":{}}`,
		"a request":             `{"jsonrpc":"2.0","id":3,"method":"elicitation/create","params":{}}`,
		"an unasked answer":     `{"jsonrpc":"2.0","id":3,"result":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			// act
			forward, _ := f.toVM([]byte(message))

			// assert
			assert.Nil(t, forward)
		})
	}
}

// Two requests with the same ID must not let the answer to one pass as
// the answer to the other.
func TestFilterTellsAnswersApartWhateverIDsTheVMGives(t *testing.T) {
	// arrange
	_, f := project(t)
	first, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{}}}`))
	require.NoError(t, err)
	second, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"ide_connected","params":{}}`))
	require.NoError(t, err)

	text := `[{"uri":"file:///Users/someone/secret/x.go","diagnostics":[{"message":"secret"}]}]`
	answer, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
	require.NoError(t, err)

	// act
	forward, _ := f.toVM(answer)

	// assert
	assert.Contains(t, string(first), `"id":1`)
	assert.Contains(t, string(second), `"id":2`)
	assert.NotContains(t, string(forward), "secret")
	assert.Contains(t, string(forward), `"id":5`)
}

func TestFilterPassesOnlyTheArgumentsItKnows(t *testing.T) {
	// arrange
	_, f := project(t)

	// act
	forward, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{"path":"/etc/passwd","uri":"file:///project/main.go"},"_meta":{"x":1}}}`))

	// assert
	require.NoError(t, err)
	assert.NotContains(t, string(forward), "/etc/passwd")
	assert.NotContains(t, string(forward), "_meta")
}

func TestFilterClosesOnlyTabsTheVMOpened(t *testing.T) {
	// arrange
	_, f := project(t)
	_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"openDiff","arguments":{"old_file_path":"/project/main.go","new_file_path":"/project/main.go","new_file_contents":"","tab_name":"mine"}}}`))
	require.NoError(t, err)

	// act
	own, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"close_tab","arguments":{"tab_name":"mine"}}}`))
	require.NoError(t, err)
	other, reply, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"close_tab","arguments":{"tab_name":"the person's"}}}`))
	require.NoError(t, err)

	// assert
	assert.NotNil(t, own)
	assert.Nil(t, other)
	assert.JSONEq(t, outside, string(reply))
}

func TestFilterForgetsTheTabsOfTheVMWhenAllDiffTabsClose(t *testing.T) {
	// arrange
	_, f := project(t)
	_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"openDiff","arguments":{"old_file_path":"/project/main.go","new_file_path":"/project/main.go","new_file_contents":"","tab_name":"mine"}}}`))
	require.NoError(t, err)
	_, _, err = f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"closeAllDiffTabs","arguments":{}}}`))
	require.NoError(t, err)

	// act
	forward, reply, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"close_tab","arguments":{"tab_name":"mine"}}}`))

	// assert
	require.NoError(t, err)
	assert.Nil(t, forward)
	assert.JSONEq(t, outside, string(reply))
}

func TestFilterTakesThePathOfTheHostOutOfAnError(t *testing.T) {
	// arrange
	host, f := project(t)
	_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{}}}`))
	require.NoError(t, err)

	answer, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -1, "message": "cannot read " + host + "/main.go"}})
	require.NoError(t, err)

	// act
	forward, _ := f.toVM(answer)

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"error":{"code":-1,"message":"cannot read /project/main.go"}}`, string(forward))
}

func TestFilterTakesThePathOfTheHostOutOfAnErrorWithEscapes(t *testing.T) {
	// arrange: some encoders escape every letter past ASCII, which a user
	// name may have, and JSON may escape any character
	host, f := project(t)
	_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"getDiagnostics","arguments":{}}}`))
	require.NoError(t, err)

	var escaped strings.Builder
	for _, r := range host + "/main.go" {
		fmt.Fprintf(&escaped, `\u%04x`, r)
	}

	// act
	forward, _ := f.toVM([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"cannot read ` + escaped.String() + `"}}`))

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"error":{"code":-1,"message":"cannot read /project/main.go"}}`, string(forward))
}

func TestFilterTakesThePathOfTheHostOutOfAnswers(t *testing.T) {
	// arrange
	host, f := project(t)
	_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"closeAllDiffTabs","arguments":{}}}`))
	require.NoError(t, err)

	answer, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -1, "message": "x", "data": map[string]any{"path": host + "/main.go"}}})
	require.NoError(t, err)

	// act
	forward, _ := f.toVM(answer)

	// assert
	assert.NotContains(t, string(forward), host)
	assert.Contains(t, string(forward), "/project/main.go")
}

func TestFilterEndsTheLinkOnAnIDTooLongToKeep(t *testing.T) {
	// arrange
	_, f := project(t)

	// act
	_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":"` + strings.Repeat("x", 2000) + `","method":"tools/list"}`))

	// assert
	assert.ErrorIs(t, err, errLongID)
}

func TestFilterHidesThePathOfTheHostButNotOfAFolderNextToIt(t *testing.T) {
	// arrange
	host, f := project(t)
	_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"closeAllDiffTabs","arguments":{}}}`))
	require.NoError(t, err)

	answer, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": host + "/a " + host + "-old/b " + host},
		map[string]any{"type": "image", "data": "x"},
	}}})
	require.NoError(t, err)

	// act
	forward, _ := f.toVM(answer)

	// assert
	want, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 5, "result": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "/project/a " + host + "-old/b /project"},
		map[string]any{"type": "image", "data": "x"},
	}}})
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(forward))
}

func TestFilterAnswersAPingOfTheIDEItself(t *testing.T) {
	// arrange
	_, f := project(t)

	// act
	forward, reply := f.toVM([]byte(`{"jsonrpc":"2.0","id":"p1","method":"ping"}`))

	// assert
	assert.Nil(t, forward)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"p1","result":{}}`, string(reply))
}

// jsonString is the text as a JSON string, so that a path of Windows with
// its backslashes fits into a message of a test.
func jsonString(text string) string {
	data, _ := json.Marshal(text)

	return string(data)
}

func TestFilterForgetsTheOldestRequestTheIDENeverAnswered(t *testing.T) {
	// arrange: the IDE never answers a notification that carries an ID, so
	// a VM could make the filter keep request after request
	_, f := project(t)

	for range maxKept + 1 {
		_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":7,"method":"notifications/initialized"}`))
		require.NoError(t, err)
	}

	// act
	oldest, _ := f.toVM([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	newest, _ := f.toVM([]byte(`{"jsonrpc":"2.0","id":` + strconv.Itoa(maxKept+1) + `,"result":{}}`))

	// assert
	assert.Nil(t, oldest, "the oldest request is forgotten")
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{}}`, string(newest))
}

func TestFilterForgetsTheOldestTabOfTheVM(t *testing.T) {
	// arrange
	_, f := project(t)

	for i := range maxKept + 1 {
		_, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"openDiff","arguments":{"old_file_path":"/project/main.go","new_file_path":"/project/main.go","new_file_contents":"","tab_name":"tab ` + strconv.Itoa(i) + `"}}}`))
		require.NoError(t, err)
	}

	// act
	oldest, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"close_tab","arguments":{"tab_name":"tab 0"}}}`))
	require.NoError(t, err)
	newest, _, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"close_tab","arguments":{"tab_name":"tab ` + strconv.Itoa(maxKept) + `"}}}`))
	require.NoError(t, err)

	// assert
	assert.Nil(t, oldest, "the oldest tab is forgotten")
	assert.NotNil(t, newest)
}
