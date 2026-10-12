package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/config"
)

func TestLoadReadsAMountOfAWindowsFolder(t *testing.T) {
	home := `C:\Users\someone`
	t.Setenv("USERPROFILE", home)

	tests := map[string]string{
		`C:\Users\someone\sdk`:         `C:\Users\someone\sdk`,
		`C:/Users/someone/sdk`:         `C:\Users\someone\sdk`,
		`\\server\share\sdk`:           `\\server\share\sdk`,
		`~\sdk`:                        `C:\Users\someone\sdk`,
		`D:\tools with a space\go\bin`: `D:\tools with a space\go\bin`,
		`\\?\C:\sdk`:                   `\\?\C:\sdk`,
		`//server/share/sdk`:           `\\server\share\sdk`,
	}

	for written, folder := range tests {
		// as it is and in single quotes, both of which the docs allow
		for _, quote := range []string{"", "'"} {
			t.Run(quote+written+quote, func(t *testing.T) {
				// arrange
				path := write(t, "mounts:\n  - "+quote+written+":/opt/go"+quote+"\n")

				// act
				cfg, err := config.Load(path, "")

				// assert
				require.NoError(t, err)
				assert.Equal(t, []config.Mount{{Host: folder, Guest: "/opt/go"}}, cfg.Mounts)
			})
		}
	}
}

func TestLoadRejectsAWindowsMountItCannotRead(t *testing.T) {
	tests := map[string]string{
		"a colon past the drive":         `C:\sdk:x:/opt/go`,
		"a folder relative to the drive": `C:sdk:/opt/go`,
		"a drive alone":                  `C:`,
		"a guest with backslashes":       `C:\sdk:\opt\go`,
		"a guest on a drive":             `C:\sdk:C:/opt/go`,
	}

	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			path := write(t, "mounts:\n  - '"+entry+"'\n")

			// act
			_, err := config.Load(path, "")

			// assert
			assert.ErrorIs(t, err, config.ErrBadMount)
		})
	}
}

// The folders of the path are of the VM, which runs Linux, whatever the
// host.
func TestLoadReadsThePathAsFoldersOfTheVM(t *testing.T) {
	// arrange
	accepted := write(t, "path:\n  - /opt/go/bin\n")
	backslashes := write(t, "path:\n  - '\\opt\\go\\bin'\n")

	// act
	cfg, err := config.Load(accepted, "")
	_, backslashesErr := config.Load(backslashes, "")

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"/opt/go/bin"}, cfg.Path)
	assert.ErrorIs(t, backslashesErr, config.ErrBadPath)
}
