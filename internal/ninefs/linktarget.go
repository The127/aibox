package ninefs

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

const (
	tagMountPoint = 0xA0000003
	tagSymlink    = 0xA000000C
	tagLxSymlink  = 0xA000001D
)

// linkTarget reads where a link points from its reparse data, as the
// guest names it: Windows' separators turned, the NT prefix gone. It
// reads the substitute name, which every link has, not the print name,
// which tools may leave empty. A reparse point is data anyone with write
// access to it set, so every length is checked against the buffer, and
// what doesn't fit is no target.
func linkTarget(b []byte) (string, bool) {
	if len(b) < 8 {
		return "", false
	}

	tag := binary.LittleEndian.Uint32(b[0:4])
	end := 8 + int(binary.LittleEndian.Uint16(b[4:6]))

	if end > len(b) {
		return "", false
	}

	data := b[8:end]

	switch tag {
	case tagLxSymlink:
		// a version, then the target as the guest gave it
		if len(data) < 4 {
			return "", false
		}

		// a NUL would end the target early in the guest
		target := string(data[4:])

		return target, target != "" && !strings.ContainsRune(target, 0)
	case tagSymlink:
		// the names follow the offsets, lengths and flags
		return windowsTarget(data, 12)
	case tagMountPoint:
		return windowsTarget(data, 8)
	default:
		return "", false
	}
}

// windowsTarget reads the substitute name of a symlink or junction whose
// path buffer starts at the offset into data.
func windowsTarget(data []byte, buffer int) (string, bool) {
	if len(data) < buffer {
		return "", false
	}

	offset := buffer + int(binary.LittleEndian.Uint16(data[0:2]))
	length := int(binary.LittleEndian.Uint16(data[2:4]))

	if length == 0 || length%2 != 0 || offset+length > len(data) {
		return "", false
	}

	units := make([]uint16, length/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[offset+2*i:])
	}

	name := string(utf16.Decode(units))

	switch {
	case strings.HasPrefix(name, `\??\UNC\`):
		name = `\\` + name[len(`\??\UNC\`):]
	case strings.HasPrefix(name, `\??\`):
		name = name[len(`\??\`):]
	}

	return strings.ReplaceAll(name, `\`, "/"), true
}
