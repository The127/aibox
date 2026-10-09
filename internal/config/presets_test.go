package config

import (
	"os"
	"path/filepath"
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
	_, err := Load(path)

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
