package ide_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/ide"
)

// writeLock writes a lock file like the one of the VS Code plugin, written
// at the time.
func writeLock(t *testing.T, dir, name, ideName string, folders []string, at time.Time) {
	t.Helper()

	data, err := json.Marshal(map[string]any{
		"pid":              4242,
		"workspaceFolders": folders,
		"ideName":          ideName,
		"transport":        "ws",
		"runningInWindows": false,
		"authToken":        "token-of-" + ideName,
	})
	require.NoError(t, err)

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, os.Chtimes(path, at, at))
}

func folder(t *testing.T, parts ...string) string {
	t.Helper()

	path := filepath.Join(parts...)
	require.NoError(t, os.MkdirAll(path, 0o700))

	return path
}

func TestFindReturnsTheIDEThatHasTheProjectOpen(t *testing.T) {
	// arrange
	dir, root := t.TempDir(), t.TempDir()
	project := folder(t, root, "aibox")
	writeLock(t, dir, "23214.lock", "Visual Studio Code", []string{project}, time.Now())

	// act
	ides, err := ide.Find(dir, project)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []ide.IDE{{Name: "Visual Studio Code", Port: 23214, Token: "token-of-Visual Studio Code"}}, ides)
}

func TestFindTakesAFolderThatHoldsTheProject(t *testing.T) {
	// arrange
	dir, root := t.TempDir(), t.TempDir()
	project := folder(t, root, "repos", "aibox")
	writeLock(t, dir, "23214.lock", "GoLand", []string{filepath.Join(root, "repos")}, time.Now())

	// act
	ides, err := ide.Find(dir, project)

	// assert
	require.NoError(t, err)
	require.Len(t, ides, 1)
	assert.Equal(t, "GoLand", ides[0].Name)
}

func TestFindLeavesOutIDEsWithoutTheProject(t *testing.T) {
	// arrange
	dir, root := t.TempDir(), t.TempDir()
	project := folder(t, root, "aibox")
	writeLock(t, dir, "1001.lock", "inside the project", []string{folder(t, project, "docs")}, time.Now())
	writeLock(t, dir, "1002.lock", "a neighbour", []string{folder(t, root, "aibox-old")}, time.Now())
	writeLock(t, dir, "1003.lock", "a relative folder", []string{"aibox"}, time.Now())
	writeLock(t, dir, "1004.lock", "a missing folder", []string{filepath.Join(root, "gone")}, time.Now())
	writeLock(t, dir, "1005.lock", "no folder", nil, time.Now())

	// act
	ides, err := ide.Find(dir, project)

	// assert
	require.NoError(t, err)
	assert.Empty(t, ides)
}

func TestFindPutsTheClosestFolderFirstThenTheNewestLockThenTheHigherPort(t *testing.T) {
	// arrange
	dir, root := t.TempDir(), t.TempDir()
	project := folder(t, root, "repos", "aibox")
	then := time.Now().Add(-time.Hour)
	writeLock(t, dir, "1001.lock", "the parent, newest", []string{filepath.Join(root, "repos")}, time.Now())
	writeLock(t, dir, "1002.lock", "the project, older", []string{project}, then)
	writeLock(t, dir, "1003.lock", "the project, newer", []string{project}, then.Add(time.Minute))
	writeLock(t, dir, "1004.lock", "the project, older, higher port", []string{project}, then)
	writeLock(t, dir, "1005.lock", "two folders, the project last", []string{root, project}, then.Add(-time.Minute))
	writeLock(t, dir, "1006.lock", "two folders, the project first", []string{project, root}, then.Add(-2*time.Minute))

	// act
	ides, err := ide.Find(dir, project)

	// assert
	require.NoError(t, err)

	names := make([]string, len(ides))
	for i, found := range ides {
		names[i] = found.Name
	}

	assert.Equal(t, []string{
		"the project, newer",
		"the project, older, higher port",
		"the project, older",
		"two folders, the project last",
		"two folders, the project first",
		"the parent, newest",
	}, names)
}

func TestFindSkipsWhatIsNoLockFileOfAWebSocket(t *testing.T) {
	// arrange
	dir, root := t.TempDir(), t.TempDir()
	project := folder(t, root, "aibox")
	files := map[string]string{
		"0123.lock":  "a port with a leading zero",
		"+80.lock":   "a port with a sign",
		"0.lock":     "port 0",
		"65536.lock": "a port too big",
		"x.lock":     "no port",
		"23214.json": "no .lock",
	}

	for name, ideName := range files {
		writeLock(t, dir, name, ideName, []string{project}, time.Now())
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "1001.lock"), []byte("{not json"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1002.lock"), []byte(`{"workspaceFolders":["`+project+`"],"transport":"sse","authToken":"t"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1003.lock"), []byte(`{"workspaceFolders":["`+project+`"],"transport":"ws"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1004.lock"), []byte(`{"workspaceFolders":["`+project+`"],"transport":"ws","authToken":"`+strings.Repeat("x", 64<<10)+`"}`), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "1005.lock"), 0o700))

	// act
	ides, err := ide.Find(dir, project)

	// assert
	require.NoError(t, err)
	assert.Empty(t, ides)
}

func TestFindReadsALockFileOfTheMostBytesAllowed(t *testing.T) {
	// arrange
	dir, root := t.TempDir(), t.TempDir()
	project := folder(t, root, "aibox")
	head := `{"workspaceFolders":["` + project + `"],"transport":"ws","authToken":"`
	tail := `"}`
	data := head + strings.Repeat("x", 64<<10-len(head)-len(tail)) + tail
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1001.lock"), []byte(data), 0o600))

	// act
	ides, err := ide.Find(dir, project)

	// assert
	require.NoError(t, err)
	assert.Len(t, ides, 1)
}

// An IDE may write the folder in another case than the terminal of aibox
// has it, where the file system tells no case apart, as on a Mac or on
// Windows.
func TestFindTakesAFolderInAnotherCase(t *testing.T) {
	// arrange
	dir, root := t.TempDir(), t.TempDir()
	project := folder(t, root, "Repos", "aibox")
	other := filepath.Join(root, "rEPOS", "aibox")

	if _, err := os.Stat(other); err != nil {
		t.Skip("the file system tells cases apart")
	}

	writeLock(t, dir, "1001.lock", "VS Code", []string{other}, time.Now())

	// act
	ides, err := ide.Find(dir, project)

	// assert
	require.NoError(t, err)
	require.Len(t, ides, 1)
	assert.Equal(t, "VS Code", ides[0].Name)
}

func TestFindWithoutTheFolderOfTheLockFilesFindsNothing(t *testing.T) {
	// act
	ides, err := ide.Find(filepath.Join(t.TempDir(), "ide"), t.TempDir())

	// assert
	require.NoError(t, err)
	assert.Empty(t, ides)
}

func TestFindFailsWhenTheFolderOfTheLockFilesCannotBeRead(t *testing.T) {
	// arrange
	file := filepath.Join(t.TempDir(), "ide")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	// act
	_, err := ide.Find(file, t.TempDir())

	// assert
	assert.ErrorContains(t, err, "read the lock files of the IDEs")
}

func TestIDEPrintsWithoutItsToken(t *testing.T) {
	// arrange
	found := ide.IDE{Name: "Visual Studio Code", Port: 23214, Token: "s3cret"}

	// act
	printed := found.String()

	// assert
	assert.Equal(t, "Visual Studio Code on port 23214", printed)
}
