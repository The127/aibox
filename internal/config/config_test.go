package config_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/config"
)

// host is the Unix path p as an absolute folder of this host: on Windows,
// on drive C.
func host(p string) string {
	if runtime.GOOS == "windows" {
		return `C:` + filepath.FromSlash(p)
	}

	return p
}

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
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.Hosts{"example.com", "*.github.com"}, cfg.Allow)
}

func TestLoadReadsTheMounts(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - "+host("/opt/sdk/go")+":/opt/go\n  - "+host("/home/someone/bin")+":/opt/bin\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: host("/opt/sdk/go"), Guest: "/opt/go"}, {Host: host("/home/someone/bin"), Guest: "/opt/bin"}}, cfg.Mounts)
}

func TestLoadCleansThePathsOfAMount(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - "+host("/opt/sdk//go/")+":/opt/go/\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: host("/opt/sdk/go"), Guest: "/opt/go"}}, cfg.Mounts)
}

func TestLoadExpandsTheHomeInAMount(t *testing.T) {
	// arrange
	home := host("/home/someone")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path := write(t, "mounts:\n  - ~/sdk/go:/opt/go\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: filepath.Join(home, "sdk", "go"), Guest: "/opt/go"}}, cfg.Mounts)
}

func TestLoadAcceptsASpaceInTheHostFolderOfAMount(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - "+host("/opt/my sdk")+":/opt/go\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: host("/opt/my sdk"), Guest: "/opt/go"}}, cfg.Mounts)
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
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrBadMount)
		})
	}
}

func TestLoadNamesTheMountItRejects(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - /opt/go\n")

	// act
	_, err := config.Load(path, "")

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
			path := write(t, "mounts:\n  - "+host("/opt/sdk")+":"+guest+"\n")

			// act
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrReservedMount)
			assert.ErrorContains(t, err, guest)
		})
	}
}

func TestLoadAcceptsAMountInsideTheHome(t *testing.T) {
	// arrange
	path := write(t, "mounts:\n  - "+host("/home/someone/.claude/skills")+":/home/user/.claude/skills\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Mount{{Host: host("/home/someone/.claude/skills"), Guest: "/home/user/.claude/skills"}}, cfg.Mounts)
}

func TestLoadRejectsMountsThatOverlap(t *testing.T) {
	tests := map[string]string{
		"the same path twice":    "mounts:\n  - " + host("/opt/a") + ":/opt/go\n  - " + host("/opt/b") + ":/opt/go\n",
		"one inside the other":   "mounts:\n  - " + host("/opt/a") + ":/opt\n  - " + host("/opt/b") + ":/opt/go\n",
		"one containing another": "mounts:\n  - " + host("/opt/b") + ":/opt/go\n  - " + host("/opt/a") + ":/opt\n",
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, content)

			// act
			_, err := config.Load(path, "")

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
		fmt.Fprintf(&content, "  - %s:/opt/%s%d\n", host(fmt.Sprintf("/opt/sdk%d", i)), strings.Repeat("x", 100), i)
	}

	path := write(t, content.String())

	// act
	_, err := config.Load(path, "")

	// assert
	assert.ErrorIs(t, err, config.ErrCmdlineFull)
}

func TestLoadWritesAMountsExampleIntoTheDefaultFile(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path, "")

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Contains(t, string(content), "# mounts:\n#   - ")
}

func TestLoadReadsTheSkills(t *testing.T) {
	for _, skills := range []config.Share{config.ShareAll, config.ShareNone} {
		t.Run(string(skills), func(t *testing.T) {
			// arrange
			path := write(t, "skills: "+string(skills)+"\n")

			// act
			cfg, err := config.Load(path, "")

			// assert
			require.NoError(t, err)
			assert.Equal(t, skills, cfg.Skills)
		})
	}
}

func TestLoadLeavesSkillsWithoutAValueAtTheDefault(t *testing.T) {
	// arrange
	path := write(t, "skills:\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Empty(t, cfg.Skills, "empty is all")
}

func TestLoadRejectsSkillsOtherThanAllOrNone(t *testing.T) {
	for _, text := range []string{"some", "false", "[pdf]"} {
		t.Run(text, func(t *testing.T) {
			// arrange
			path := write(t, "skills: "+text+"\n")

			// act
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrBadShare)
		})
	}
}

func TestLoadWritesASkillsExampleIntoTheDefaultFile(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path, "")

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)
	assert.Contains(t, string(content), "# skills: none\n")
	assert.Contains(t, string(content), "# ide: true\n")
}

func TestLoadReadsWhetherTheIDEIsTurnedOn(t *testing.T) {
	for content, want := range map[string]bool{"ide: true\n": true, "ide: false\n": false, "": false} {
		t.Run(content, func(t *testing.T) {
			// arrange
			path := write(t, content)

			// act
			cfg, err := config.Load(path, "")

			// assert
			require.NoError(t, err)
			assert.Equal(t, want, cfg.IDE)
		})
	}
}

func TestLoadReadsTheName(t *testing.T) {
	for content, want := range map[string]string{"name: path\n": "path", "name: network config\n": "network config", "": ""} {
		t.Run(content, func(t *testing.T) {
			// arrange
			path := write(t, content)

			// act
			cfg, err := config.Load(path, "")

			// assert
			require.NoError(t, err)
			assert.Equal(t, want, cfg.Name)
		})
	}
}

func TestLoadReadsThePath(t *testing.T) {
	// arrange
	path := write(t, "path:\n  - /opt/go/bin\n  - /opt/bin/\n  - PATH\n  - DIRENV_PATH\n")

	// act
	cfg, err := config.Load(path, "")

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
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrBadPath)
		})
	}
}

func TestLoadReadsWatch(t *testing.T) {
	// arrange
	path := write(t, "watch:\n  - Makefile\n  - .github/workflows/\n  - ./scripts\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"Makefile", ".github/workflows", "scripts"}, cfg.Watch)
}

func TestLoadRejectsAWatchEntryOutsideTheProject(t *testing.T) {
	tests := map[string]string{
		"absolute":          "/etc/passwd",
		"above":             "../other",
		"above in disguise": "scripts/../../other",
		"empty":             "",
		"the project":       ".",
		"the project again": "scripts/..",
		"control character": "Makefile\\u0001",
	}

	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, "watch:\n  - \""+entry+"\"\n")

			// act
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrBadWatch)
		})
	}
}

func TestLoadNamesThePathEntryItRejects(t *testing.T) {
	// arrange
	path := write(t, "path:\n  - opt/bin\n")

	// act
	_, err := config.Load(path, "")

	// assert
	assert.ErrorContains(t, err, "opt/bin")
}

func TestLoadWritesAPathExampleIntoTheDefaultFile(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path, "")

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
	cfg, err := config.Load(path, "")

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
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrBadVariable)
		})
	}
}

func TestLoadRejectsAVariableAiboxSetsItself(t *testing.T) {
	for _, name := range []string{"HOME", "PATH", "TERM", "HTTPS_PROXY", "no_proxy", "AIBOX", "USER", "GOMODCACHE", "AIBOX_SESSION_NAME"} {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, "env:\n  - "+name+"=x\n")

			// act
			_, err := config.Load(path, "")

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
	_, err := config.Load(path, "")

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
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.Default(), cfg)

	// Windows has no such modes, the ACL of the home folder keeps it
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestLoadReadsTheDefaultFileBackAsTheDefault(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")
	_, err := config.Load(path, "")
	require.NoError(t, err)

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.Default(), cfg)
}

func TestLoadReadsTheDiskSize(t *testing.T) {
	// arrange
	path := write(t, "disk: 4\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	require.NotNil(t, cfg.Disk)
	assert.Equal(t, 4, *cfg.Disk)
}

func TestLoadRejectsADiskSizeBelowOne(t *testing.T) {
	// arrange
	path := write(t, "disk: 0\n")

	// act
	_, err := config.Load(path, "")

	// assert
	assert.ErrorIs(t, err, config.ErrNotPositive)
}

func TestLoadRejectsADiskSizeThatDoesNotFitAFile(t *testing.T) {
	// arrange
	path := write(t, "disk: 1048577\n")

	// act
	_, err := config.Load(path, "")

	// assert
	assert.ErrorIs(t, err, config.ErrDiskTooLarge)
}

func TestLoadAcceptsTheSmallestAndLargestSizes(t *testing.T) {
	for _, content := range []string{"disk: 1\n", "disk: 1048576\n", "memory: 1\n", "cpus: 1\n"} {
		t.Run(content, func(t *testing.T) {
			// arrange
			path := write(t, content)

			// act
			_, err := config.Load(path, "")

			// assert
			assert.NoError(t, err)
		})
	}
}

func TestLoadWritesTheDiskKeyAsAnExample(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path, "")

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
	cfg, err := config.Load(path, "")

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
	cfg, err := config.Load(path, "")

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
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrNotPositive)
		})
	}
}

func TestLoadWritesTheSizeKeysAsExamples(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := config.Load(path, "")

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
	cfg, err := config.Load(path, "")

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
			cfg, err := config.Load(path, "")

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
	_, err := config.Load(path, "")

	// assert
	assert.ErrorContains(t, err, path)
}

func TestLoadRejectsAnUnknownKey(t *testing.T) {
	// arrange
	path := write(t, "alow:\n  - example.com\n")

	// act
	_, err := config.Load(path, "")

	// assert
	assert.ErrorContains(t, err, "alow")
}

func TestLoadStoresEntriesInOneForm(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - EXAMPLE.com:443\n  - \"[::1]:8443\"\n  - Git.Example.:22\n  - \"*.GitHub.com\"\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.Hosts{"example.com", "[::1]:8443", "git.example:22", "*.github.com"}, cfg.Allow)
}

func TestLoadExpandsAPreset(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - preset:go\n  - example.com\n")

	// act
	cfg, err := config.Load(path, "")

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
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.ElementsMatch(t, config.Hosts{"registry-1.docker.io", "auth.docker.io", "index.docker.io", "production.cloudfront.docker.com"}, cfg.Allow)
}

func TestLoadRejectsAnUnknownPreset(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - preset:rust\n")

	// act
	_, err := config.Load(path, "")

	// assert
	assert.ErrorIs(t, err, config.ErrUnknownPreset)
	assert.ErrorContains(t, err, "rust")
	assert.ErrorContains(t, err, "cargo, docker, github, go, mise, npm, pypi")
}

func TestLoadRejectsAnEntryThatCannotMatch(t *testing.T) {
	entries := []string{
		"*", "*.", "https://example.com", "a b", "",
		"example.com:", "example.com:x", "example.com:0", "example.com:0443", "example.com:+443", "example.com:65536", "example.com:70000",
		"[::1]", "::1", "a:b:c",
	}

	for _, entry := range entries {
		t.Run(entry, func(t *testing.T) {
			// arrange
			path := write(t, "allow:\n  - \""+entry+"\"\n")

			// act
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrBadHost)
		})
	}
}

func TestLoadAcceptsTheSmallestAndLargestPort(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - example.com:1\n  - example.com:65535\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.Hosts{"example.com:1", "example.com:65535"}, cfg.Allow)
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
	hosts := config.Hosts{"127.0.0.1:64422", "[::1]:8080", "example.com:8080", "10.0.0.5:22", "127.0.0.2:9000", "127.0.0.1:64422", "127.0.0.1"}

	// act
	ports := hosts.LoopbackPorts()

	// assert
	assert.Equal(t, []uint16{443, 8080, 64422}, ports)
}

func TestRemotePortsAreThePortsOfTheEntriesOffTheLoopback(t *testing.T) {
	// arrange
	hosts := config.Hosts{"127.0.0.1:64422", "[::1]:8080", "example.com:8080", "10.0.0.5:22", "127.0.0.2:9000", "example.org"}

	// act
	ports := hosts.RemotePorts()

	// assert
	assert.Equal(t, []uint16{22, 443, 8080, 9000}, ports)
}

func TestPinned(t *testing.T) {
	// arrange
	hosts := config.Hosts{"127.0.0.1:64422", "[::1]:8080", "127.0.0.2:9000", "example.com:9000"}

	tests := []struct {
		host, port string
		want       []net.IP
	}{
		{"localhost", "64422", []net.IP{net.IPv4(127, 0, 0, 1)}},
		{"LOCALHOST.", "64422", []net.IP{net.IPv4(127, 0, 0, 1)}},
		{"localhost", "8080", []net.IP{net.IPv6loopback}},
		{"localhost", "9000", nil},
		{"localhost", "443", nil},
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
	hosts := config.Hosts{"127.0.0.1:64422", "[::1]:8080", "*.localhost:22", "127.0.0.2:9000"}

	// act and assert
	assert.True(t, hosts.Allows("localhost", "64422"))
	assert.True(t, hosts.Allows("localhost", "8080"))
	assert.False(t, hosts.Allows("localhost", "22"))
	assert.False(t, hosts.Allows("localhost", "9000"))
	assert.False(t, hosts.Allows("localhost", "443"))
}

func TestLoadRejectsAnEntryForLocalhost(t *testing.T) {
	for _, entry := range []string{"localhost", "localhost:8123", "LocalHost.:8123"} {
		t.Run(entry, func(t *testing.T) {
			// arrange
			path := write(t, "allow:\n  - "+entry+"\n")

			// act
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrLocalhost)
		})
	}
}

func TestLoadRejectsAnUnspecifiedAddress(t *testing.T) {
	for _, entry := range []string{"0.0.0.0:8123", "\"[::]:8123\"", "0.0.0.0"} {
		t.Run(entry, func(t *testing.T) {
			// arrange
			path := write(t, "allow:\n  - "+entry+"\n")

			// act
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrUnspecified)
		})
	}
}

func TestLoadRejectsAPortOnBothAddressesOfTheLoopback(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - 127.0.0.1:8123\n  - \"[::1]:8123\"\n")

	// act
	_, err := config.Load(path, "")

	// assert
	assert.ErrorIs(t, err, config.ErrLoopbackTwice)
}

func TestLoadAcceptsTheSameLoopbackEntryTwice(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - 127.0.0.1:8123\n  - 127.0.0.1:8123\n  - \"[::1]:8080\"\n")

	// act
	_, err := config.Load(path, "")

	// assert
	assert.NoError(t, err)
}

func TestLoadRejectsTheProxyPortOnTheLoopback(t *testing.T) {
	for _, entry := range []string{"127.0.0.1:3128", "\"[::1]:3128\""} {
		t.Run(entry, func(t *testing.T) {
			// arrange
			path := write(t, "allow:\n  - "+entry+"\n")

			// act
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrProxyPort)
		})
	}
}

func TestLoadAcceptsTheProxyPortElsewhere(t *testing.T) {
	// arrange
	path := write(t, "allow:\n  - proxy.example:3128\n")

	// act
	_, err := config.Load(path, "")

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
	_, err := config.Load(path, "")

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

func TestEnsureDefaultStartsFromTheTemplate(t *testing.T) {
	// arrange
	dir := t.TempDir()
	template := filepath.Join(dir, "template.yaml")
	require.NoError(t, os.WriteFile(template, []byte("# mine\nallow:\n  - preset:go\nmemory: 4096\n"), 0o600))
	path := filepath.Join(dir, "config.yaml")

	// act
	cfg, err := config.Load(path, template)

	// assert
	require.NoError(t, err)
	assert.Equal(t, 4096, *cfg.Memory)

	written, err := os.ReadFile(path) //nolint:gosec // a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "# mine\nallow:\n  - preset:go\nmemory: 4096\n", string(written), "the comments come along")
}

func TestEnsureDefaultLeavesAConfigThatIsThere(t *testing.T) {
	// arrange
	dir := t.TempDir()
	template := filepath.Join(dir, "template.yaml")
	require.NoError(t, os.WriteFile(template, []byte("memory: 4096\n"), 0o600))
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("cpus: 4\n"), 0o600))

	// act
	err := config.EnsureDefault(path, template)

	// assert
	require.NoError(t, err)
	written, err := os.ReadFile(path) //nolint:gosec // a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "cpus: 4\n", string(written))
}

func TestEnsureDefaultRefusesATemplateWithAMistake(t *testing.T) {
	// arrange
	dir := t.TempDir()
	template := filepath.Join(dir, "template.yaml")
	require.NoError(t, os.WriteFile(template, []byte("allow:\n  - preset:nothing\n"), 0o600))
	path := filepath.Join(dir, "config.yaml")

	// act
	err := config.EnsureDefault(path, template)

	// assert
	require.ErrorIs(t, err, config.ErrUnknownPreset)
	assert.ErrorContains(t, err, "the template "+template)
	assert.NoFileExists(t, path, "no project starts from it")
}

func TestEnsureDefaultWithoutATemplateWritesTheDefault(t *testing.T) {
	// arrange
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// act
	cfg, err := config.Load(path, filepath.Join(dir, "none.yaml"))

	// assert
	require.NoError(t, err)
	assert.True(t, cfg.Allow.Allows("api.anthropic.com", "443"))
}

func TestLoadReadsTheSharesOfTheClaudeSetup(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agents: none\ncommands: all\nclaude-md: none\nsettings: none\n"), 0o600))

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, config.ShareNone, cfg.Agents)
	assert.Equal(t, config.ShareAll, cfg.Commands)
	assert.Equal(t, config.ShareNone, cfg.ClaudeMD)
	assert.Equal(t, config.ShareNone, cfg.Settings)
}

func TestLoadRefusesAShareOtherThanAllOrNone(t *testing.T) {
	for _, field := range []string{"agents", "commands", "claude-md", "settings"} {
		t.Run(field, func(t *testing.T) {
			// arrange
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte("memory: 4096\n"+field+": some\n"), 0o600))

			// act
			_, err := config.Load(path, "")

			// assert
			require.ErrorIs(t, err, config.ErrBadShare)
			assert.ErrorContains(t, err, "line 2")
		})
	}
}

func TestLoadReadsThePorts(t *testing.T) {
	// arrange
	path := write(t, "ports:\n  - 5173\n  - 8080:3000\n  - \"9000:9001\"\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, []config.Port{{Host: 5173, VM: 5173}, {Host: 8080, VM: 3000}, {Host: 9000, VM: 9001}}, cfg.Ports)
}

func TestLoadRejectsABadPort(t *testing.T) {
	for _, entry := range []string{"0", "65536", "http", "8080:", ":3000", "1:2:3", "-1", "8080:0"} {
		// arrange
		path := write(t, "ports:\n  - \""+entry+"\"\n")

		// act
		_, err := config.Load(path, "")

		// assert
		require.ErrorIs(t, err, config.ErrBadPort, entry)
	}
}

func TestLoadRejectsAPortOfTheHostTwice(t *testing.T) {
	// arrange
	path := write(t, "ports:\n  - 5173\n  - 5173:3000\n")

	// act
	_, err := config.Load(path, "")

	// assert
	require.ErrorIs(t, err, config.ErrPortTwice)
}

func TestLoadReadsMaxConnections(t *testing.T) {
	// arrange
	path := write(t, "max-connections: 64\n")

	// act
	cfg, err := config.Load(path, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, 64, cfg.ConnectionLimit())
}

func TestConnectionLimitHasADefault(t *testing.T) {
	// act and assert
	assert.Equal(t, config.DefaultMaxConnections, config.Config{}.ConnectionLimit())
}

func TestLoadRejectsMaxConnectionsBelowOne(t *testing.T) {
	// arrange
	path := write(t, "max-connections: 0\n")

	// act
	_, err := config.Load(path, "")

	// assert
	require.ErrorIs(t, err, config.ErrNotPositive)
}

func TestAWildcardMatchesNoAddress(t *testing.T) {
	// arrange
	hosts := config.Hosts{"*.3.4", "*.githubusercontent.com"}

	for _, host := range []string{"::ffff:1.2.3.4", "1.2.3.4", "::ffff:1.2.3.4%25.githubusercontent.com"} {
		// act and assert
		assert.False(t, hosts.Allows(host, "443"), host)
	}

	assert.True(t, hosts.Allows("objects.githubusercontent.com", "443"))
}
