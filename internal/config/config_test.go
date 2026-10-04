package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/config"
)

// write puts a config file with the content into a temp folder.
func write(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func TestLoadReadsTheAllowList(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - Example.com.\n  - \"*.github.com\"\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.Hosts{"example.com", "*.github.com"}, cfg.Allow)
}

func TestLoadWritesTheDefaultFileWhenThereIsNone(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.Default(), cfg)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestLoadReadsTheDefaultFileBackAsTheDefault(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")
	_, err := config.Load(path)
	require.NoError(t, err)

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.Default(), cfg)
}

func TestLoadReadsMemoryAndCPUs(t *testing.T) {
	// arrange
	path := write(t, "allow: []\nmemory: 4096\ncpus: 4\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	require.NotNil(t, cfg.Memory)
	require.NotNil(t, cfg.CPUs)
	assert.Equal(t, 4096, *cfg.Memory)
	assert.Equal(t, 4, *cfg.CPUs)
}

func TestLoadLeavesMemoryAndCPUsNilWhenNotGiven(t *testing.T) {
	// arrange
	path := write(t, "allow: []\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Nil(t, cfg.Memory)
	assert.Nil(t, cfg.CPUs)
}

func TestLoadRejectsMemoryOrCPUsBelowOne(t *testing.T) {
	for _, content := range []string{"memory: 0\n", "memory: -1\n", "cpus: 0\n", "cpus: -2\n"} {
		t.Run(content, func(t *testing.T) {
			// arrange
			path := write(t, content)

			// act
			_, err := config.Load(path)

			// assert
			assert.ErrorIs(t, err, config.ErrNotPositive)
		})
	}
}

func TestLoadWritesTheSizeKeysAsExamples(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Contains(t, string(content), "# memory:")
}

func TestLoadKeepsAnExistingFile(t *testing.T) {
	// arrange
	path := write(t, "allow: []\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Empty(t, cfg.Allow)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "allow: []\n", string(content))
}

func TestLoadTreatsAFileWithoutSettingsAsEmpty(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "comments only": "# nothing allowed\n"} {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, content)

			// act
			cfg, err := config.Load(path)

			// assert
			require.NoError(t, err)
			assert.Empty(t, cfg.Allow)
		})
	}
}

func TestLoadRejectsABrokenFile(t *testing.T) {
	// arrange
	path := write(t, "allow: [unclosed\n")

	// act
	_, err := config.Load(path)

	// assert
	assert.ErrorContains(t, err, path)
}

func TestLoadRejectsAnUnknownKey(t *testing.T) {
	// arrange
	path := write(t, "alow:\n  - example.com\n")

	// act
	_, err := config.Load(path)

	// assert
	assert.ErrorContains(t, err, "alow")
}

func TestLoadStoresEntriesInOneForm(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - EXAMPLE.com:443\n  - \"[::1]:8443\"\n  - Git.Example.:22\n  - \"*.GitHub.com\"\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.Hosts{"example.com", "[::1]:8443", "git.example:22", "*.github.com"}, cfg.Allow)
}

func TestLoadRejectsAnEntryThatCannotMatch(t *testing.T) {
	entries := []string{
		"*", "*.", "https://example.com", "a b", "",
		"example.com:", "example.com:x", "example.com:0", "example.com:0443", "example.com:+443", "example.com:70000",
		"[::1]", "::1", "a:b:c",
	}

	for _, entry := range entries {
		t.Run(entry, func(t *testing.T) {
			// arrange
			path := write(t, "allow:\n  - \""+entry+"\"\n")

			// act
			_, err := config.Load(path)

			// assert
			assert.ErrorIs(t, err, config.ErrBadHost)
		})
	}
}

func TestAllows(t *testing.T) {
	// arrange
	hosts := config.Hosts{"example.com", "*.github.com", "10.0.0.5", "git.example:22", "[::1]:8443"}

	tests := []struct {
		host, port string
		want       bool
	}{
		{"example.com", "443", true},
		{"EXAMPLE.com", "443", true},
		{"example.com.", "443", true},
		{"example.com", "80", false},
		{"www.example.com", "443", false},
		{"api.github.com", "443", true},
		{"a.b.github.com", "443", true},
		{"github.com", "443", false},
		{"notgithub.com", "443", false},
		{"evil.com", "443", false},
		{"10.0.0.5", "443", true},
		{"git.example", "22", true},
		{"git.example", "443", false},
		{"::1", "8443", true},
		{"", "443", false},
	}

	for _, test := range tests {
		t.Run(test.host+":"+test.port, func(t *testing.T) {
			// act
			allowed := hosts.Allows(test.host, test.port)

			// assert
			assert.Equal(t, test.want, allowed)
		})
	}
}
