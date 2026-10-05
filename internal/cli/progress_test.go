package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTheProgressOfADownloadIsOneLineCountedInMB(t *testing.T) {
	// arrange
	var out bytes.Buffer

	show := showProgress(&out)

	// act
	for _, done := range []int64{0, 1 << 10, 1 << 20, 3 << 19, 2 << 20} {
		show(done, 2<<20)
	}

	// assert
	assert.Equal(t, "\raibox: 0 of 2 MB\raibox: 1 of 2 MB\raibox: 2 of 2 MB\n", out.String())
}
