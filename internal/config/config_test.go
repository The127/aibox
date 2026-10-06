package config_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadReadsTheMounts(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - /opt/sdk/go:/opt/go\n  - /home/someone/bin:/opt/bin\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: "/opt/sdk/go", Guest: "/opt/go"}, {Host: "/home/someone/bin", Guest: "/opt/bin"}}, cfg.Mounts)
}

func TestLoadCleansThePathsOfAMount(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - /opt/sdk//go/:/opt/go/\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: "/opt/sdk/go", Guest: "/opt/go"}}, cfg.Mounts)
}

func TestLoadExpandsTheHomeInAMount(t *testing.T) {
	// arrange
	t.Setenv("HOME", "/home/someone")
	path := write(t, "mounts:\n  - ~/sdk/go:/opt/go\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: "/home/someone/sdk/go", Guest: "/opt/go"}}, cfg.Mounts)
}

func TestLoadAcceptsASpaceInTheHostFolderOfAMount(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - /opt/my sdk:/opt/go\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: "/opt/my sdk", Guest: "/opt/go"}}, cfg.Mounts)
}

func TestLoadRejectsAMountItCannotRead(t *testing.T) {
	tests := map[string]string{
		"no colon":                   "/opt/go",
		"empty host":                 ":/opt/go",
		"empty guest":                "/opt/go:",
		"relative host":              "sdk/go:/opt/go",
		"relative guest":             "/opt/go:opt/go",
		"two colons":                 "/opt/go:/opt/go:x",
		"space in guest":             "/opt/go:/opt/my go",
		"quote in guest":             "/opt/go:/opt/\\\"go",
		"control character in guest": "/opt/go:/opt/go\\u0001",
	}

	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, "mounts:\n  - \""+entry+"\"\n")

			// act
			_, err := config.Load(path)

			// assert
			assert.ErrorIs(t, err, config.ErrBadMount)
		})
	}
}

func TestLoadNamesTheMountItRejects(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - /opt/go\n")

	// act
	_, err := config.Load(path)

	// assert
	assert.ErrorContains(t, err, "/opt/go")
}

func TestLoadRejectsAMountOnAPathTheVMNeeds(t *testing.T) {
	tests := map[string]string{
		"the root":             "/",
		"the project":          "/project",
		"inside the project":   "/project/vendor",
		"the home":             "/home/user",
		"above the home":       "/home",
		"a kernel file system": "/dev",
		"inside one":           "/dev/shm",
		"the programs":         "/usr",
		"inside the programs":  "/usr/local/bin",
		"the certificates":     "/etc/ssl",
		"the root user's home": "/root",
	}

	for name, guest := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, "mounts:\n  - /opt/sdk:"+guest+"\n")

			// act
			_, err := config.Load(path)

			// assert
			assert.ErrorIs(t, err, config.ErrReservedMount)
			assert.ErrorContains(t, err, guest)
		})
	}
}

func TestLoadAcceptsAMountInsideTheHome(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - /home/someone/.claude/skills:/home/user/.claude/skills\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: "/home/someone/.claude/skills", Guest: "/home/user/.claude/skills"}}, cfg.Mounts)
}

func TestLoadRejectsMountsThatOverlap(t *testing.T) {
	tests := map[string]string{
		"the same path twice":    "mounts:\n  - /opt/a:/opt/go\n  - /opt/b:/opt/go\n",
		"one inside the other":   "mounts:\n  - /opt/a:/opt\n  - /opt/b:/opt/go\n",
		"one containing another": "mounts:\n  - /opt/b:/opt/go\n  - /opt/a:/opt\n",
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, content)

			// act
			_, err := config.Load(path)

			// assert
			assert.ErrorIs(t, err, config.ErrMountOverlap)
			assert.ErrorContains(t, err, "/opt/go")
		})
	}
}

func TestLoadRejectsMountsTheKernelCommandLineCannotCarry(t *testing.T) {
	// arrange
	var content strings.Builder

	content.WriteString("mounts:\n")

	for i := range 10 {
		fmt.Fprintf(&content, "  - /opt/sdk%d:/opt/%s%d\n", i, strings.Repeat("x", 100), i)
	}

	path := write(t, content.String())

	// act
	_, err := config.Load(path)

	// assert
	assert.ErrorIs(t, err, config.ErrCmdlineFull)
}

func TestLoadWritesAMountsExampleIntoTheDefaultFile(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Contains(t, string(content), "# mounts:\n#   - ")
}

func TestLoadReadsThePath(t *testing.T) {
	// arrange
	path := write(t, "path:\n  - /opt/go/bin\n  - /opt/bin/\n  - PATH\n  - DIRENV_PATH\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"/opt/go/bin", "/opt/bin", "PATH", "DIRENV_PATH"}, cfg.Path)
}

func TestLoadRejectsAPathEntryThatIsNeitherAFolderNorAVariable(t *testing.T) {
	tests := map[string]string{
		"relative folder":      "opt/bin",
		"empty":                "",
		"the root":             "/",
		"the root in disguise": "/opt/..",
		"two folders":          "/opt/bin:/opt/go/bin",
		"folder with a colon":  "/opt/a:b",
		"dash in the name":     "MY-PATH",
		"space in the name":    "MY PATH",
		"control character":    "/opt/bin\\u0001",
	}

	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, "path:\n  - \""+entry+"\"\n")

			// act
			_, err := config.Load(path)

			// assert
			assert.ErrorIs(t, err, config.ErrBadPath)
		})
	}
}

func TestLoadNamesThePathEntryItRejects(t *testing.T) {
	// arrange
	path := write(t, "path:\n  - opt/bin\n")

	// act
	_, err := config.Load(path)

	// assert
	assert.ErrorContains(t, err, "opt/bin")
}

func TestLoadWritesAPathExampleIntoTheDefaultFile(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Contains(t, string(content), "# path:\n#   - ")
}

func TestLoadReadsTheEnv(t *testing.T) {
	// arrange
	path := write(t, "env:\n  - GOFLAGS=-mod=mod\n  - TOKEN=a=b\n  - GITHUB_TOKEN\n  - empty=\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Variable{
		{Name: "GOFLAGS", Value: "-mod=mod"},
		{Name: "TOKEN", Value: "a=b"},
		{Name: "GITHUB_TOKEN", FromHost: true},
		{Name: "empty", Value: ""},
	}, cfg.Env)
}

func TestLoadRejectsAVariableItCannotRead(t *testing.T) {
	tests := map[string]string{
		"no name":                        "=x",
		"empty":                          "",
		"name starting with a digit":     "1ABC=x",
		"space in the name":              "A B=x",
		"dash in the name":               "A-B",
		"control character in the value": "A=B=C\\u0001",
	}

	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, "env:\n  - \""+entry+"\"\n")

			// act
			_, err := config.Load(path)

			// assert
			assert.ErrorIs(t, err, config.ErrBadVariable)
		})
	}
}

func TestLoadRejectsAVariableAiboxSetsItself(t *testing.T) {
	for _, name := range []string{"HOME", "PATH", "TERM", "HTTPS_PROXY", "no_proxy", "AIBOX", "USER", "GOMODCACHE"} {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, "env:\n  - "+name+"=x\n")

			// act
			_, err := config.Load(path)

			// assert
			assert.ErrorIs(t, err, config.ErrReservedVariable)
			assert.ErrorContains(t, err, name)
		})
	}
}

func TestLoadWritesAnEnvExampleIntoTheDefaultFile(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Contains(t, string(content), "# env:\n#   - ")
}

func TestDefaultHasNoHostForUpdates(t *testing.T) {
	// act
	cfg := config.Default()

	// assert
	assert.NotContains(t, cfg.Allow, "downloads.claude.ai")
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

func TestLoadReadsTheDiskSize(t *testing.T) {
	// arrange
	path := write(t, "disk: 4\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	require.NotNil(t, cfg.Disk)
	assert.Equal(t, 4, *cfg.Disk)
}

func TestLoadRejectsADiskSizeBelowOne(t *testing.T) {
	// arrange
	path := write(t, "disk: 0\n")

	// act
	_, err := config.Load(path)

	// assert
	assert.ErrorIs(t, err, config.ErrNotPositive)
}

func TestLoadRejectsADiskSizeThatDoesNotFitAFile(t *testing.T) {
	// arrange
	path := write(t, "disk: 1048577\n")

	// act
	_, err := config.Load(path)

	// assert
	assert.ErrorIs(t, err, config.ErrDiskTooLarge)
}

func TestLoadWritesTheDiskKeyAsAnExample(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Contains(t, string(content), "# disk:")
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

func TestLoadExpandsAPreset(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - preset:go\n  - example.com\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.Contains(t, cfg.Allow, "proxy.golang.org")
	assert.Contains(t, cfg.Allow, "storage.googleapis.com")
	assert.Contains(t, cfg.Allow, "sum.golang.org")
	assert.Contains(t, cfg.Allow, "example.com")
	assert.NotContains(t, cfg.Allow, "preset:go")
	assert.True(t, cfg.Allow.Allows("proxy.golang.org", "443"))
}

func TestLoadExpandsTheDockerPreset(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - preset:docker\n")

	// act
	cfg, err := config.Load(path)

	// assert
	require.NoError(t, err)
	assert.ElementsMatch(t, config.Hosts{"registry-1.docker.io", "auth.docker.io", "index.docker.io", "production.cloudfront.docker.com"}, cfg.Allow)
}

func TestLoadRejectsAnUnknownPreset(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - preset:rust\n")

	// act
	_, err := config.Load(path)

	// assert
	assert.ErrorIs(t, err, config.ErrUnknownPreset)
	assert.ErrorContains(t, err, "rust")
	assert.ErrorContains(t, err, "cargo, docker, github, go, npm, pypi")
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

func TestPortsAreTheDistinctPortsOfTheAllowList(t *testing.T) {
	// arrange
	hosts := config.Hosts{"example.com", "git.example:22", "[::1]:8443", "other.example:22", "api.example"}

	// act
	ports := hosts.Ports()

	// assert
	assert.Equal(t, []uint16{22, 443, 8443}, ports)
}

func TestLoopbackPortsAreThePortsOfTheEntriesOnTheLoopback(t *testing.T) {
	// arrange
	hosts := config.Hosts{"127.0.0.1:64422", "[::1]:8080", "localhost:3000", "example.com:8080", "10.0.0.5:22", "127.0.0.1:64422", "127.0.0.1"}

	// act
	ports := hosts.LoopbackPorts()

	// assert
	assert.Equal(t, []uint16{443, 3000, 8080, 64422}, ports)
}

func TestPinned(t *testing.T) {
	// arrange
	hosts := config.Hosts{"127.0.0.1:64422", "[::1]:8080", "127.0.0.1:8080", "localhost:3000", "example.com:9000"}
	v4, v6 := net.IPv4(127, 0, 0, 1), net.IPv6loopback

	tests := []struct {
		host, port string
		want       []net.IP
	}{
		{"localhost", "64422", []net.IP{v4}},
		{"LOCALHOST.", "64422", []net.IP{v4}},
		{"localhost", "8080", []net.IP{v6, v4}},
		{"localhost", "3000", []net.IP{v4, v6}},
		{"localhost", "9000", nil},
		{"example.com", "9000", nil},
		{"127.0.0.1", "64422", nil},
	}

	for _, test := range tests {
		t.Run(test.host+":"+test.port, func(t *testing.T) {
			// act
			addresses := hosts.Pinned(test.host, test.port)

			// assert
			assert.Equal(t, test.want, addresses)
		})
	}
}

func TestAllowsLocalhostOnTheLoopbackPorts(t *testing.T) {
	// arrange
	hosts := config.Hosts{"127.0.0.1:64422", "[::1]:8080", "*.localhost:22"}

	// act and assert
	assert.True(t, hosts.Allows("localhost", "64422"))
	assert.True(t, hosts.Allows("localhost", "8080"))
	assert.False(t, hosts.Allows("localhost", "22"))
	assert.False(t, hosts.Allows("localhost", "443"))
}

func TestLoadRejectsTheProxyPortOnTheLoopback(t *testing.T) {
	for _, entry := range []string{"127.0.0.1:3128", "localhost:3128", "\"[::1]:3128\""} {
		t.Run(entry, func(t *testing.T) {
			// arrange
			path := write(t, "allow:\n  - "+entry+"\n")

			// act
			_, err := config.Load(path)

			// assert
			assert.ErrorIs(t, err, config.ErrProxyPort)
		})
	}
}

func TestLoadAcceptsTheProxyPortElsewhere(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - proxy.example:3128\n")

	// act
	_, err := config.Load(path)

	// assert
	assert.NoError(t, err)
}

func TestLoadRejectsLoopbackPortsTheKernelCommandLineCannotCarry(t *testing.T) {
	// arrange
	var content strings.Builder

	content.WriteString("allow:\n")

	for port := range 200 {
		fmt.Fprintf(&content, "  - 127.0.0.1:%d\n", 10000+port)
	}

	path := write(t, content.String())

	// act
	_, err := config.Load(path)

	// assert
	assert.ErrorIs(t, err, config.ErrCmdlineFull)
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
