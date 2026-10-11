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

func TestDynamicMakesAnEmptyDiskWhoseBlocksAreAllToCome(t *testing.T) {
	t.Parallel()

	// arrange: a disk of three blocks and a bit
	path := filepath.Join(t.TempDir(), "state.ext4")
	size := int64(3*blockSize + sectorSize)

	// act
	err := Dynamic(path, size)

	// assert
	require.NoError(t, err)

	b, err := os.ReadFile(path) //nolint:gosec // the path is the test's own
	require.NoError(t, err)

	// a copy of the footer, the header, a table of four entries in one
	// sector, and the footer
	require.Len(t, b, FooterSize+headerSize+sectorSize+FooterSize)
	assert.Equal(t, b[:FooterSize], b[len(b)-FooterSize:], "the footer and its copy")

	var f [FooterSize]byte
	copy(f[:], b[:FooterSize])
	assert.Equal(t, "conectix", string(f[0:8]))
	assert.Equal(t, uint32(diskTypeDynamic), binary.BigEndian.Uint32(f[60:64]))
	assert.Equal(t, uint64(FooterSize), binary.BigEndian.Uint64(f[16:24]), "the header follows the footer's copy")
	assert.Equal(t, uint64(size), binary.BigEndian.Uint64(f[48:56]))
	assert.Equal(t, checksum(f), binary.BigEndian.Uint32(f[64:68]))

	h := b[FooterSize : FooterSize+headerSize]
	assert.Equal(t, "cxsparse", string(h[0:8]))
	assert.Equal(t, uint64(FooterSize+headerSize), binary.BigEndian.Uint64(h[16:24]), "the table follows the header")
	assert.Equal(t, uint32(4), binary.BigEndian.Uint32(h[28:32]), "entries")
	assert.Equal(t, uint32(blockSize), binary.BigEndian.Uint32(h[32:36]))

	var sum uint32
	for i, c := range h {
		if i < 36 || i >= 40 {
			sum += uint32(c)
		}
	}

	assert.Equal(t, ^sum, binary.BigEndian.Uint32(h[36:40]), "the header's checksum")

	table := b[FooterSize+headerSize : FooterSize+headerSize+sectorSize]
	for i := range 4 {
		assert.Equal(t, uint32(unallocated), binary.BigEndian.Uint32(table[i*4:]), "block %d", i)
	}

	assert.Equal(t, make([]byte, sectorSize-16), table[16:], "the rest of the table's sector")

	is, err := IsDynamic(path)
	require.NoError(t, err)
	assert.True(t, is)

	assert.NoFileExists(t, path+".tmp", "the disk was written beside its path and moved there")
}

func TestDynamicMakesATableOfManySectorsForTheDefaultDisk(t *testing.T) {
	t.Parallel()

	// arrange: 16 GiB, whose 8192 entries take 64 sectors
	path := filepath.Join(t.TempDir(), "state.ext4")

	// act
	err := Dynamic(path, 16<<30)

	// assert
	require.NoError(t, err)

	b, err := os.ReadFile(path) //nolint:gosec // the path is the test's own
	require.NoError(t, err)
	require.Len(t, b, FooterSize+headerSize+8192*4+FooterSize)

	table := b[FooterSize+headerSize : len(b)-FooterSize]
	assert.Equal(t, uint32(unallocated), binary.BigEndian.Uint32(table[0:4]), "the first block")
	assert.Equal(t, uint32(unallocated), binary.BigEndian.Uint32(table[len(table)-4:]), "the last block")
	assert.Equal(t, uint32(8192), binary.BigEndian.Uint32(b[FooterSize+28:FooterSize+32]), "entries")
}

func TestDynamicRefusesWhatNoDynamicVHDHoldsAndLeavesNothing(t *testing.T) {
	t.Parallel()

	// arrange
	path := filepath.Join(t.TempDir(), "state.ext4")

	// act: the table's entries point into the file by sectors of a uint32,
	// so 2 TiB of blocks and their bitmaps are past its end
	err := Dynamic(path, 2<<40)

	// assert
	assert.ErrorIs(t, err, ErrTooLarge)
	assert.NoFileExists(t, path)
	assert.NoFileExists(t, path+".tmp")
}

func TestDynamicRefusesASizeThatIsNoSectors(t *testing.T) {
	t.Parallel()

	tests := map[string]int64{"nothing": 0, "a sector and a bit": sectorSize + 1, "less than nothing": -sectorSize}

	for name, size := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			path := filepath.Join(t.TempDir(), "state.ext4")

			// act
			err := Dynamic(path, size)

			// assert
			assert.ErrorIs(t, err, ErrNotSectors)
			assert.NoFileExists(t, path)
		})
	}
}

func TestDynamicRefusesAFileThatHoldsData(t *testing.T) {
	t.Parallel()

	// arrange
	path := filepath.Join(t.TempDir(), "state.ext4")
	require.NoError(t, os.WriteFile(path, []byte("ext4"), 0o600))

	// act
	err := Dynamic(path, blockSize)

	// assert
	assert.ErrorIs(t, err, ErrNotEmpty)
}

func TestIsDynamicTellsADynamicVHDFromTheRest(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		make    func(t *testing.T, path string)
		dynamic bool
	}{
		"a dynamic VHD": {func(t *testing.T, path string) {
			t.Helper()
			require.NoError(t, Dynamic(path, blockSize))
		}, true},
		"a fixed VHD": {func(t *testing.T, path string) {
			t.Helper()
			require.NoError(t, os.WriteFile(path, make([]byte, 1<<20), 0o600))
			require.NoError(t, Append(path))
		}, false},
		"a raw image": {func(t *testing.T, path string) {
			t.Helper()
			require.NoError(t, os.WriteFile(path, make([]byte, 1<<20), 0o600))
		}, false},
		"a file shorter than a footer": {func(t *testing.T, path string) {
			t.Helper()
			require.NoError(t, os.WriteFile(path, []byte("conectix"), 0o600))
		}, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			path := filepath.Join(t.TempDir(), "disk")
			tt.make(t, path)

			// act
			is, err := IsDynamic(path)

			// assert
			require.NoError(t, err)
			assert.Equal(t, tt.dynamic, is)
		})
	}
}

func TestIsDynamicTellsOfAFileThatIsNotThere(t *testing.T) {
	t.Parallel()

	// act
	_, err := IsDynamic(filepath.Join(t.TempDir(), "none"))

	// assert
	assert.ErrorIs(t, err, os.ErrNotExist)
}
