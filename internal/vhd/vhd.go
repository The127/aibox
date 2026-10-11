// Package vhd makes a raw disk image a fixed VHD, which Hyper-V attaches,
// by appending the footer the VHD specification describes. The guest sees
// the same bytes as before: a fixed VHD is its data and then the footer.
package vhd

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// FooterSize is the size of the footer.
const FooterSize = 512

// sectorSize is the size of a sector, which the data of a VHD is made of.
const sectorSize = 512

// The fields of the footer, as the specification lays them out.
const (
	cookie         = "conectix"
	featureAlways  = 2          // the reserved bit, always set
	formatVersion  = 0x00010000 // 1.0
	noDataOffset   = ^uint64(0) // a fixed disk has no further structures
	creatorApp     = "aibx"
	creatorVersion = 0x00010000
	creatorHostOS  = "Wi2k"
	diskTypeFixed  = 2
)

// epoch is where the time stamp of the footer counts from.
var epoch = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

// ErrNotSectors is a size that isn't a whole number of sectors.
var ErrNotSectors = errors.New("not a whole number of 512-byte sectors")

// ErrAlreadyVHD is an image that ends in a footer already.
var ErrAlreadyVHD = errors.New("the image is a VHD already")

// Footer is the footer of a fixed VHD of size bytes of data, with the id
// and made at now.
func Footer(size int64, id [16]byte, now time.Time) ([FooterSize]byte, error) {
	var f [FooterSize]byte

	if size <= 0 || size%sectorSize != 0 {
		return f, fmt.Errorf("%w: %d bytes", ErrNotSectors, size)
	}

	copy(f[0:8], cookie)
	binary.BigEndian.PutUint32(f[8:12], featureAlways)
	binary.BigEndian.PutUint32(f[12:16], formatVersion)
	binary.BigEndian.PutUint64(f[16:24], noDataOffset)
	binary.BigEndian.PutUint32(f[24:28], uint32(now.Sub(epoch)/time.Second)) //nolint:gosec // the field holds 136 years from 2000
	copy(f[28:32], creatorApp)
	binary.BigEndian.PutUint32(f[32:36], creatorVersion)
	copy(f[36:40], creatorHostOS)
	binary.BigEndian.PutUint64(f[40:48], uint64(size)) // original size
	binary.BigEndian.PutUint64(f[48:56], uint64(size)) // current size

	g := geometryOf(size / sectorSize)
	binary.BigEndian.PutUint16(f[56:58], g.cylinders)
	f[58] = g.heads
	f[59] = g.sectorsPerTrack

	binary.BigEndian.PutUint32(f[60:64], diskTypeFixed)
	copy(f[68:84], id[:])

	binary.BigEndian.PutUint32(f[64:68], checksum(f))

	return f, nil
}

// checksum is the one's complement of the sum of the bytes of the footer,
// its own field taken as zero.
func checksum(f [FooterSize]byte) uint32 {
	var sum uint32

	for i, b := range f {
		if i >= 64 && i < 68 {
			continue
		}

		sum += uint32(b)
	}

	return ^sum
}

// geometry is a disk's size in cylinders, heads and sectors per track,
// which the footer must hold although nothing reads a VHD by it.
type geometry struct {
	cylinders       uint16
	heads           uint8
	sectorsPerTrack uint8
}

// The most of each part of a geometry: the size of its field, and for the
// heads the most the specification's algorithm gives.
const (
	maxCylinders       = 65535
	maxHeads           = 16
	maxSectorsPerTrack = 255
)

// geometryOf is the geometry the specification computes for the sectors,
// in its appendix, whose numbers it takes as they are. The largest
// geometry stands for any larger disk.
func geometryOf(sectors int64) geometry {
	sectors = min(sectors, maxCylinders*maxHeads*maxSectorsPerTrack)

	if sectors >= maxCylinders*maxHeads*63 {
		return geometry{cylinders: uint16(sectors / maxSectorsPerTrack / maxHeads), heads: maxHeads, sectorsPerTrack: maxSectorsPerTrack}
	}

	spt := int64(17)
	cylTimesHeads := sectors / spt
	heads := max((cylTimesHeads+1023)/1024, 4)

	if cylTimesHeads >= heads*1024 || heads > maxHeads {
		spt, heads = 31, maxHeads
		cylTimesHeads = sectors / spt
	}

	if cylTimesHeads >= heads*1024 {
		spt, heads = 63, maxHeads
		cylTimesHeads = sectors / spt
	}

	return geometry{
		cylinders:       uint16(cylTimesHeads / heads), //nolint:gosec // below 65535 by the branches above
		heads:           uint8(heads),                  //nolint:gosec // 16 at most
		sectorsPerTrack: uint8(spt),
	}
}

// Append makes the raw image at path a fixed VHD in place.
func Append(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // the image the caller makes a VHD
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return fmt.Errorf("stat %s: %w", path, err)
	}

	err = appendFooter(f, info.Size())
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", path, closeErr)
	}

	return err
}

func appendFooter(f *os.File, size int64) error {
	// an image appended to twice would be a VHD whose data ends in a
	// footer, which the guest would take for the end of its file system
	if size >= FooterSize {
		tail := make([]byte, len(cookie))
		if _, err := f.ReadAt(tail, size-FooterSize); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read the end of %s: %w", f.Name(), err)
		}

		if string(tail) == cookie {
			return fmt.Errorf("%w: %s", ErrAlreadyVHD, f.Name())
		}
	}

	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("make the id of the disk: %w", err)
	}

	footer, err := Footer(size, id, time.Now())
	if err != nil {
		return err
	}

	if _, err := f.WriteAt(footer[:], size); err != nil {
		return fmt.Errorf("write the footer to %s: %w", f.Name(), err)
	}

	return nil
}
