package realpath

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDOSPathDropsOnlyThePrefixOfALongPath(t *testing.T) {
	tests := map[string]string{
		`\\?\C:\x`:                `C:\x`,
		`\\?\C:\`:                 `C:\`,
		`\\?\UNC\server\share\x`:  `\\server\share\x`,
		`\\?\Volume{1234-abcd}\x`: `\\?\Volume{1234-abcd}\x`,
		`C:\x`:                    `C:\x`,
	}

	for final, want := range tests {
		t.Run(final, func(t *testing.T) {
			assert.Equal(t, want, dosPath(final))
		})
	}
}
