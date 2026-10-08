package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTheProgressOfADownloadIsOneLineCountedInMB(t *testing.T) {
	// arrange
	var out bytes.Buffer

	show, end := showProgress(&out)

	// act: the last MB arrives before the end, and the read at the end of
	// the body reports the end again
	for _, done := range []int64{0, 1 << 10, 1 << 20, 3 << 19, 2 << 20, 5 << 19, 5 << 19} {
		show(done, 5<<19)
	}

	end()

	// assert
	assert.Equal(t, "\raibox: 0 of 2 MB\raibox: 1 of 2 MB\raibox: 2 of 2 MB\n", out.String())
}

func TestTheProgressOfADownloadEndsItsLine(t *testing.T) {
	for name, calls := range map[string]func(show func(done, total int64)){
		"of an unknown size": func(show func(done, total int64)) {
			show(0, -1)
			show(5<<20, -1)
		},
		"that failed": func(show func(done, total int64)) {
			show(70<<20, 142<<20)
		},
	} {
		t.Run(name, func(t *testing.T) {
			// arrange
			var out bytes.Buffer

			show, end := showProgress(&out)
			calls(show)

			// act
			end()

			// assert
			assert.Regexp(t, `MB\n$`, out.String())
		})
	}
}

func TestTheProgressOfADownloadWritesNothingWithoutADownload(t *testing.T) {
	// arrange
	var out bytes.Buffer

	_, end := showProgress(&out)

	// act
	end()

	// assert
	assert.Empty(t, out.String())
}
