package ide

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSummaryKeepsANameFromTheVMOnOneShortLine(t *testing.T) {
	tests := map[string]string{
		`{"method":"tools/call","params":{"name":"executeCode"}}`:                         `tools/call "executeCode"`,
		`{"method":"tools/call","params":{"name":"x\n2026-10-11T00:00:00Z ide: forged"}}`: `tools/call "x\n2026-10-11T00:00:00Z ide: forged"`,
		`{"method":"` + strings.Repeat("m", 200) + `"}`:                                   `"` + strings.Repeat("m", 100) + `..."`,
		`{"id":1,"result":{}}`: "an answer",
		`[1]`:                  "a message that is no JSON-RPC",
	}

	for message, want := range tests {
		t.Run(want, func(t *testing.T) {
			// act
			got := summary([]byte(message))

			// assert
			assert.Equal(t, want, got)
			assert.NotContains(t, got, "\n")
		})
	}
}
