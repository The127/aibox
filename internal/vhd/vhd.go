// Package vhd writes the VHDs Hyper-V attaches, as the VHD specification
// lays them out: it makes a raw disk image a fixed VHD by appending the
// footer, so that the guest sees the same bytes as before, and makes an
// empty dynamic VHD, which takes up space as the guest fills it.
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
	return footer(size, id, now, diskTypeFixed, noDataOffset)
}

// footer is the footer of a VHD of the type, of size bytes as the guest
// sees them, whose further structures, if any, start at dataOffset.
func footer(size int64, id [16]byte, now time.Time, diskType uint32, dataOffset uint64) ([FooterSize]byte, error) {
	var f [FooterSize]byte

	if size <= 0 || size%sectorSize != 0 {
		return f, fmt.Errorf("%w: %d bytes", ErrNotSectors, size)
	}

	copy(f[0:8], cookie)
	binary.BigEndian.PutUint32(f[8:12], featureAlways)
	binary.BigEndian.PutUint32(f[12:16], formatVersion)
	binary.BigEndian.PutUint64(f[16:24], dataOffset)
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

	binary.BigEndian.PutUint32(f[60:64], diskType)
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
	if ok, err := hasFooter(f, size); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrAlreadyVHD, f.Name())
	}

	id, err := newID()
	if err != nil {
		return err
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

// readFooter reads the footer the file of size bytes ends in, and tells
// whether it ends in one.
func readFooter(f *os.File, size int64) ([FooterSize]byte, bool, error) {
	var footer [FooterSize]byte

	if size < FooterSize {
		return footer, false, nil
	}

	if _, err := f.ReadAt(footer[:], size-FooterSize); err != nil && !errors.Is(err, io.EOF) {
		return footer, false, fmt.Errorf("read the end of %s: %w", f.Name(), err)
	}

	return footer, string(footer[:len(cookie)]) == cookie, nil
}

// hasFooter tells whether the file of size bytes ends in a footer.
func hasFooter(f *os.File, size int64) (bool, error) {
	_, ok, err := readFooter(f, size)

	return ok, err
}

// IsDynamic tells whether the file at path is a dynamic VHD: one that
// ends in a footer which says so.
func IsDynamic(path string) (bool, error) {
	f, err := os.Open(path) //nolint:gosec // the disk the caller asks about
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}

	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}

	footer, ok, err := readFooter(f, info.Size())
	if err != nil || !ok {
		return false, err
	}

	return binary.BigEndian.Uint32(footer[60:64]) == diskTypeDynamic, nil
}

// newID is the id of a new disk.
func newID() ([16]byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("make the id of the disk: %w", err)
	}

	return id, nil
}

// The structures of a dynamic VHD, as the specification lays them out: a
// copy of the footer first, then the header, then the table of where each
// block of the disk is in the file, then the footer. A block is in the
// file only once the guest wrote to it, so the VHD itself takes up space
// as it fills: Hyper-V refuses a VHD whose file is sparse.
const (
	diskTypeDynamic = 3
	dynamicCookie   = "cxsparse"
	headerSize      = 1024
	headerVersion   = 0x00010000
	// blockSize is the specification's default, which every reader takes
	blockSize = 2 << 20
	// unallocated is the table's entry for a block not in the file
	unallocated = 0xFFFFFFFF
)

var (
	// ErrNotEmpty is a file that is to become a dynamic VHD but holds data
	// already, which the VHD would have to carry over and doesn't.
	ErrNotEmpty = errors.New("the file is not empty")
	// ErrTooLarge is a size more than a dynamic VHD holds.
	ErrTooLarge = errors.New("more than a dynamic VHD holds")
)

// Dynamic makes a dynamic VHD at path, of size bytes as the guest sees
// them, all zero, which takes up space on the host as the guest writes.
// The file must not be there, or be empty. The VHD is written beside
// path and moved there whole, so that a crash leaves no half of one.
func Dynamic(path string, size int64) error {
	if info, err := os.Stat(path); err == nil && info.Size() != 0 {
		return fmt.Errorf("%w: %s", ErrNotEmpty, path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	image, err := dynamicImage(size)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // beside the disk the caller asks for
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}

	err = writeAll(f, image)
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", tmp, closeErr)
	}

	if err == nil {
		err = os.Rename(tmp, path)
	}

	if err != nil {
		_ = os.Remove(tmp)

		return err
	}

	return nil
}

// writeAll writes the image to the file, onto the disk.
func writeAll(f *os.File, image []byte) error {
	if _, err := f.Write(image); err != nil {
		return fmt.Errorf("write %s: %w", f.Name(), err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", f.Name(), err)
	}

	return nil
}

// dynamicImage is the file of an empty dynamic VHD of size bytes.
func dynamicImage(size int64) ([]byte, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}

	footer, err := footer(size, id, time.Now(), diskTypeDynamic, FooterSize)
	if err != nil {
		return nil, err
	}

	blocks := (size + blockSize - 1) / blockSize
	table := make([]byte, roundUp(blocks*4, sectorSize))

	// an entry of the table is the sector of its block in the file, a
	// uint32, which is as far as the file of a dynamic VHD reaches; each
	// block in the file comes after a bitmap of its sectors, in whole
	// sectors
	const blockSectors = blockSize / sectorSize

	bitmapSectors := roundUp(blockSectors/8, sectorSize) / sectorSize
	structures := int64(FooterSize+headerSize+len(table)+FooterSize) / sectorSize

	if structures+blocks*(bitmapSectors+blockSectors) > 1<<32 {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, size)
	}

	for i := range blocks {
		binary.BigEndian.PutUint32(table[i*4:], unallocated)
	}

	header := dynamicHeader(FooterSize+headerSize, uint32(blocks)) //nolint:gosec // fewer than the sectors of the file, which fit above

	image := make([]byte, 0, 2*FooterSize+headerSize+len(table))
	image = append(image, footer[:]...)
	image = append(image, header[:]...)
	image = append(image, table...)
	image = append(image, footer[:]...)

	return image, nil
}

// dynamicHeader is the header of a dynamic VHD whose table of blocks
// entries long starts at tableOffset, with no parent.
func dynamicHeader(tableOffset uint64, entries uint32) [headerSize]byte {
	var h [headerSize]byte

	copy(h[0:8], dynamicCookie)
	binary.BigEndian.PutUint64(h[8:16], noDataOffset)
	binary.BigEndian.PutUint64(h[16:24], tableOffset)
	binary.BigEndian.PutUint32(h[24:28], headerVersion)
	binary.BigEndian.PutUint32(h[28:32], entries)
	binary.BigEndian.PutUint32(h[32:36], blockSize)

	var sum uint32
	for _, b := range h {
		sum += uint32(b)
	}

	binary.BigEndian.PutUint32(h[36:40], ^sum)

	return h
}

// roundUp is n rounded up to a multiple of to.
func roundUp(n, to int64) int64 {
	return (n + to - 1) / to * to
}
