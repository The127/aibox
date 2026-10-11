package clipboard_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/clipboard"
)

func TestParseDataReadsTheImageOsascriptPrinted(t *testing.T) {
	// act
	image, err := clipboard.ParseData([]byte("«data PNGf89504E470D0A1A0A»\n"))

	// assert
	require.NoError(t, err)
	assert.Equal(t, []byte("\x89PNG\r\n\x1a\n"), image)
}

func TestParseDataRefusesWhatIsNoImage(t *testing.T) {
	for name, out := range map[string]string{
		"text":       "some text\n",
		"other data": "«data TIFF4D4D»\n",
		"not closed": "«data PNGf8950",
		"not hex":    "«data PNGfXYZ»",
		"odd digits": "«data PNGf895»",
	} {
		t.Run(name, func(t *testing.T) {
			// act
			_, err := clipboard.ParseData([]byte(out))

			// assert
			assert.Error(t, err)
		})
	}
}
