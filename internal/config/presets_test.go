package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPresetsHoldHostsInTheirStoredForm(t *testing.T) {
	for name, hosts := range presets {
		t.Run(name, func(t *testing.T) {
			// arrange
			require.NotEmpty(t, hosts)

			for _, host := range hosts {
				// act
				e, err := parseEntry(host)

				// assert
				require.NoError(t, err)
				assert.Equal(t, host, e.String())
			}
		})
	}
}

func TestDefaultFileNamesEveryPreset(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "config.yaml")

	// act
	_, err := Load(path, "")

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(path) //nolint:gosec // the path is a temp file of the test
	require.NoError(t, err)

	for name := range presets {
		assert.Contains(t, string(content), name)
	}
}

func TestDocsNameEveryPreset(t *testing.T) {
	// arrange
	docs, err := os.ReadFile("../../docs/config.md")
	require.NoError(t, err)

	for name := range presets {
		// assert
		assert.Contains(t, string(docs), "preset:"+name)
	}
}

func TestEveryMarkerIsOfAPresetWithATitle(t *testing.T) {
	for name, files := range markers {
		// assert
		assert.Contains(t, presets, name)
		assert.NotEmpty(t, files)
		assert.NotEmpty(t, titles[name])
	}
}

func TestDocsNameEveryMarker(t *testing.T) {
	// arrange
	docs, err := os.ReadFile("../../docs/config.md")
	require.NoError(t, err)

	for _, file := range Markers() {
		// assert
		assert.Contains(t, string(docs), "`"+file+"`")
	}
}

func TestSuggest(t *testing.T) {
	// arrange
	allow, err := expand(Hosts{"api.anthropic.com", "preset:npm"})
	require.NoError(t, err)

	// act
	suggestions := Suggest([]string{"go.mod", "go.sum", "package.json", "README.md", "Cargo.lock", "GO.MOD"}, allow)

	// assert
	assert.Equal(t, []Suggestion{
		{Preset: "cargo", Tool: "Rust", Files: []string{"Cargo.lock"}, Hosts: presets["cargo"]},
		{Preset: "go", Tool: "Go", Files: []string{"go.mod"}, Hosts: presets["go"]},
	}, suggestions, "npm is allowed already, and names match exactly")
}

func TestSuggestNothing(t *testing.T) {
	// act
	suggestions := Suggest([]string{"README.md", "Makefile"}, nil)

	// assert
	assert.Empty(t, suggestions)
}

func TestPresetFor(t *testing.T) {
	// arrange
	tests := map[string]struct {
		host, port, want string
	}{
		"a host of a preset":       {"proxy.golang.org", "443", "go"},
		"a wildcard of a preset":   {"objects.githubusercontent.com", "443", "github"},
		"in another case":          {"Registry.NPMJS.org.", "443", "npm"},
		"on another port":          {"proxy.golang.org", "80", ""},
		"a host of no preset":      {"example.com", "443", ""},
		"the domain of a wildcard": {"githubusercontent.com", "443", ""},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// act
			preset, ok := PresetFor(test.host, test.port)

			// assert
			assert.Equal(t, test.want, preset)
			assert.Equal(t, test.want != "", ok)
		})
	}
}

func TestAddPresetsKeepsEveryOtherLine(t *testing.T) {
	// arrange
	content := []byte(strings.Replace(defaultFile, "PRESETS", "go", 1))

	// act
	added, err := AddPresets(content, []string{"go", "npm"})

	// assert
	require.NoError(t, err)
	assert.Equal(t, strings.Replace(string(content),
		"  - code.claude.com           # documentation lookups\n",
		"  - code.claude.com           # documentation lookups\n  - preset:go\n  - preset:npm\n", 1), string(added))

	cfg, err := parse(added)
	require.NoError(t, err)
	assert.True(t, cfg.Allow.Allows("proxy.golang.org", "443"))
	assert.True(t, cfg.Allow.Allows("registry.npmjs.org", "443"))
}

func TestAddPresetsToAListAtTheEnd(t *testing.T) {
	// act
	added, err := AddPresets([]byte("memory: 4096\nallow:\n- example.com"), []string{"go"})

	// assert
	require.NoError(t, err)
	assert.Equal(t, "memory: 4096\nallow:\n- example.com\n- preset:go\n", string(added))
}

func TestAddPresetsKeepsTheLineEndingsAndComments(t *testing.T) {
	// arrange
	tests := map[string]struct{ content, want string }{
		"windows line endings": {
			"allow:\r\n  - example.com\r\n",
			"allow:\r\n  - example.com\r\n  - preset:go\r\n",
		},
		"a comment on the last entry": {
			"allow:\n  - example.com # mine\n",
			"allow:\n  - example.com # mine\n  - preset:go\n",
		},
		"a comment after the list": {
			"allow:\n  - example.com\n# memory: 4096\n",
			"allow:\n  - example.com\n  - preset:go\n# memory: 4096\n",
		},
		"a quoted last entry": {
			"allow:\n  - \"*.example.com\"\n",
			"allow:\n  - \"*.example.com\"\n  - preset:go\n",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// act
			added, err := AddPresets([]byte(test.content), []string{"go"})

			// assert
			require.NoError(t, err)
			assert.Equal(t, test.want, string(added))
		})
	}
}

func TestAddPresetsRefusesAListOfAnotherForm(t *testing.T) {
	// arrange
	tests := map[string]string{
		"no list":                   "memory: 4096\n",
		"an empty list":             "allow:\n",
		"a list on one line":        "allow: [example.com]\n",
		"an empty flow list":        "allow: []\n",
		"an alias at the end":       "allow:\n  - &h example.com\n  - *h\n",
		"a block scalar entry":      "allow:\n  - |\n    example.com\n",
		"nothing":                   "",
		"old mac line endings":      "allow:\r  - example.com\r",
		"a last entry on two lines": "allow:\n  - a.example.com\n  - \"b.example\\\n    .com\"\n",
		"an entry below its dash":   "allow:\n  -\n    example.com\n",
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			// act
			_, err := AddPresets([]byte(content), []string{"go"})

			// assert
			assert.ErrorIs(t, err, ErrNoAllowList)
		})
	}
}

func TestAllowHint(t *testing.T) {
	// arrange
	tests := map[string]struct{ target, want string }{
		"a host of a preset": {"proxy.golang.org:443", "Add it to config.yaml to allow it. preset:go allows it."},
		"a host of none":     {"example.com:443", "Add it to config.yaml to allow it."},
		"no host and port":   {"example.com", "Add it to config.yaml to allow it."},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// act
			hint := AllowHint("config.yaml")(test.target)

			// assert
			assert.Equal(t, test.want, hint)
		})
	}
}

func TestSuggestOffersMiseForItsFiles(t *testing.T) {
	// act
	suggestions := Suggest([]string{".tool-versions"}, nil)

	// assert
	assert.Equal(t, []Suggestion{{Preset: "mise", Tool: "mise", Files: []string{".tool-versions"}, Hosts: presets["mise"]}}, suggestions)
}
