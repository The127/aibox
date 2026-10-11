package vhd

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFooterDescribesAFixedDiskOfTheSize(t *testing.T) {
	t.Parallel()

	// arrange
	id := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	now := epoch.Add(1000 * time.Second)

	// act
	f, err := Footer(10<<20, id, now)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "conectix", string(f[0:8]))
	assert.Equal(t, uint64(10<<20), binary.BigEndian.Uint64(f[40:48]), "original size")
	assert.Equal(t, uint64(10<<20), binary.BigEndian.Uint64(f[48:56]), "current size")
	assert.Equal(t, uint32(diskTypeFixed), binary.BigEndian.Uint32(f[60:64]))
	assert.Equal(t, noDataOffset, binary.BigEndian.Uint64(f[16:24]))
	assert.Equal(t, uint32(1000), binary.BigEndian.Uint32(f[24:28]))
	assert.Equal(t, id[:], f[68:84])
	assert.Equal(t, checksum(f), binary.BigEndian.Uint32(f[64:68]))
}

func TestChecksumIsTheComplementOfTheSumOfTheOtherBytes(t *testing.T) {
	t.Parallel()

	// arrange
	var f [FooterSize]byte
	f[0], f[511] = 1, 2
	f[64] = 0xFF // the field itself counts as zero

	// act
	sum := checksum(f)

	// assert
	assert.Equal(t, ^uint32(3), sum)
}

func TestFooterRefusesASizeThatIsNotWholeSectors(t *testing.T) {
	t.Parallel()

	for _, size := range []int64{0, -512, 511, 513} {
		// act
		_, err := Footer(size, [16]byte{}, epoch)

		// assert
		assert.ErrorIs(t, err, ErrNotSectors, "size %d", size)
	}
}

func TestGeometryIsTheOneOfTheSpecification(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		sectors int64
		want    geometry
	}{
		"10 MiB, 17 sectors a track":  {10 << 20 / sectorSize, geometry{301, 4, 17}},
		"2 GiB, 63 sectors a track":   {2 << 30 / sectorSize, geometry{4161, 16, 63}},
		"64 GiB, 255 sectors a track": {64 << 30 / sectorSize, geometry{32896, 16, 255}},
		"2 TiB, beyond the largest":   {2 << 40 / sectorSize, geometry{65535, 16, 255}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			g := geometryOf(tt.sectors)

			// assert
			assert.Equal(t, tt.want, g)
		})
	}
}

func TestAppendMakesTheImageAVHDAndKeepsItsData(t *testing.T) {
	t.Parallel()

	// arrange
	path := filepath.Join(t.TempDir(), "os.vhd")
	data := make([]byte, 1<<20)
	data[0], data[len(data)-1] = 0xAB, 0xCD
	require.NoError(t, os.WriteFile(path, data, 0o600))

	// act
	err := Append(path)

	// assert
	require.NoError(t, err)

	got, err := os.ReadFile(path) //nolint:gosec // a temp file of the test
	require.NoError(t, err)
	require.Len(t, got, len(data)+FooterSize)
	assert.Equal(t, data, got[:len(data)])

	var footer [FooterSize]byte
	copy(footer[:], got[len(data):])
	assert.Equal(t, "conectix", string(footer[0:8]))
	assert.Equal(t, uint64(len(data)), binary.BigEndian.Uint64(footer[48:56]))
	assert.Equal(t, checksum(footer), binary.BigEndian.Uint32(footer[64:68]))
}

func TestAppendRefusesAnImageThatIsAVHDAlready(t *testing.T) {
	t.Parallel()

	// arrange
	path := filepath.Join(t.TempDir(), "os.vhd")
	require.NoError(t, os.WriteFile(path, make([]byte, 1<<20), 0o600))
	require.NoError(t, Append(path))

	// act
	err := Append(path)

	// assert
	assert.ErrorIs(t, err, ErrAlreadyVHD)
}
