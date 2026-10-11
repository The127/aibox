package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeHostLock writes the lock file an IDE with the folder open writes on
// the host, into dir.
func writeHostLock(t *testing.T, dir, folder string) {
	t.Helper()

	data, err := json.Marshal(map[string]any{
		"pid":              4242,
		"workspaceFolders": []string{folder},
		"ideName":          "Visual Studio Code",
		"transport":        "ws",
		"authToken":        "token-of-the-ide",
	})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "23214.lock"), data, 0o600))
}

// vmLock reads the lock file in the home of the VM.
func vmLock(t *testing.T, f *fixture) map[string]any {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(f.project(t).Home, ".claude", "ide", "3131.lock"))
	require.NoError(t, err)

	var lock map[string]any
	require.NoError(t, json.Unmarshal(data, &lock))

	return lock
}

func TestRunLinksTheIDEThatHasTheProjectOpen(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	writeHostLock(t, filepath.Join(f.homeDir, ".claude", "ide"), f.cwd)

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)

	lock := vmLock(t, f)
	assert.Equal(t, []any{"/project"}, lock["workspaceFolders"])
	assert.Equal(t, "Visual Studio Code", lock["ideName"])
	assert.Equal(t, "ws", lock["transport"])
	assert.NotEmpty(t, lock["authToken"])
	assert.NotEqual(t, "token-of-the-ide", lock["authToken"])
	assert.NotContains(t, lock, "pid")

	spec := f.launch.spec
	assert.Contains(t, spec.Env, "CLAUDE_CODE_SSE_PORT=3131")
	assert.Equal(t, []uint16{3131}, spec.Loopback)
	assert.Contains(t, spec.Ports, uint16(23214))
	require.NotNil(t, spec.Proxy.Local)
	assert.NotNil(t, spec.Proxy.Local("localhost", "3131"))
}

func TestRunFindsTheIDEInTheFolderOfClaudeConfigDir(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	configDir := t.TempDir()
	writeHostLock(t, filepath.Join(configDir, "ide"), f.cwd)
	f.deps.lookupEnv = func(name string) (string, bool) { return configDir, name == "CLAUDE_CONFIG_DIR" }

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.launch.spec.Env, "CLAUDE_CODE_SSE_PORT=3131")
}

func TestRunWithoutAnIDELinksNone(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	writeHostLock(t, filepath.Join(f.homeDir, ".claude", "ide"), t.TempDir())

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(f.project(t).Home, ".claude", "ide", "3131.lock"))
	assert.NotContains(t, f.launch.spec.Env, "CLAUDE_CODE_SSE_PORT=3131")
	assert.Empty(t, f.launch.spec.Loopback)
	assert.Nil(t, f.launch.spec.Proxy.Local)
}

func TestRunRefusesWhatTheIDELinkNeedsEvenWithoutAnIDE(t *testing.T) {
	tests := map[string]struct {
		config string
		want   error
	}{
		"the loopback port":    {"allow:\n  - 127.0.0.1:3131\n", errIDEPort},
		"CLAUDE_CODE_SSE_PORT": {"env:\n  - CLAUDE_CODE_SSE_PORT=1234\n", errOwnSSEPort},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newFixture(t)
			image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
			f.writeConfig(t, test.config)

			// act
			err := f.run("--image", image)

			// assert
			require.ErrorIs(t, err, test.want)
			assert.False(t, f.launch.called)
		})
	}
}

func TestRunReplacesTheLockFileOfAnEarlierRun(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	writeHostLock(t, filepath.Join(f.homeDir, ".claude", "ide"), f.cwd)
	require.NoError(t, f.run("--image", image))
	earlier := vmLock(t, f)["authToken"]

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.NotEqual(t, earlier, vmLock(t, f)["authToken"])

	info, err := os.Stat(filepath.Join(f.project(t).Home, ".claude", "ide", "3131.lock"))
	require.NoError(t, err)

	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}
