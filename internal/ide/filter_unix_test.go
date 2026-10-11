//go:build !windows

package ide

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A symlink in the project that the VM put there must not show the VM what
// the person selects in the file it leads to.
func TestFilterKeepsASelectionBehindASymlinkFromTheVM(t *testing.T) {
	// arrange
	host, f := project(t)
	secret := filepath.Join(t.TempDir(), "credentials")
	require.NoError(t, os.WriteFile(secret, []byte("key"), 0o600))
	require.NoError(t, os.Symlink(secret, filepath.Join(host, "notes.md")))

	// act
	forward, _ := f.toVM([]byte(`{"jsonrpc":"2.0","method":"selection_changed","params":{"text":"key","filePath":"` + filepath.Join(host, "notes.md") + `"}}`))

	// assert
	assert.Nil(t, forward)
}

// A symlink that leads to nothing yet would make the IDE create the file
// it leads to, outside the project.
func TestFilterRefusesASymlinkThatLeadsNowhere(t *testing.T) {
	// arrange
	host, f := project(t)
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "new"), filepath.Join(host, "x")))

	// act
	forward, reply, err := f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"openDiff","arguments":{"old_file_path":"/project/x","new_file_path":"/project/x","new_file_contents":"","tab_name":"t"}}}`))

	// assert
	require.NoError(t, err)
	assert.Nil(t, forward)
	assert.JSONEq(t, outside, string(reply))
}

// The IDE may name the project by the path its symlinks lead to.
func TestFilterTakesASelectionInTheProjectBehindASymlinkedPath(t *testing.T) {
	// arrange
	target, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(target, "main.go"), nil, 0o600))

	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, link))

	f := newFilter(paths{host: link})

	// act
	forward, _ := f.toVM([]byte(`{"jsonrpc":"2.0","method":"selection_changed","params":{"text":"x","filePath":"` + filepath.Join(target, "main.go") + `"}}`))

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","method":"selection_changed","params":{"text":"x","filePath":"/project/main.go"}}`, string(forward))
}

func TestFilterHidesThePathOfTheHostWithAnAmpersandAndBehindASymlink(t *testing.T) {
	// arrange
	target, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	project := filepath.Join(target, "me & you")
	require.NoError(t, os.Mkdir(project, 0o700))

	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(project, link))

	f := newFilter(paths{host: link})
	_, _, err = f.toIDE([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"closeAllDiffTabs","arguments":{}}}`))
	require.NoError(t, err)

	// act: an IDE writes & as it is
	forward, _ := f.toVM([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"` + project + `/a and ` + link + `/b"}}`))

	// assert
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"error":{"code":-1,"message":"/project/a and /project/b"}}`, string(forward))
}
