package ninefs

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
)

// windowsLink is the reparse data of a Windows link with the substitute
// and the print name; symlinks have flags before the names, junctions
// don't.
func windowsLink(tag uint32, substitute, printName string) []byte {
	sub := utf16.Encode([]rune(substitute))
	prt := utf16.Encode([]rune(printName))

	header := 8
	if tag == tagSymlink {
		header = 12
	}

	data := make([]byte, header+2*(len(sub)+len(prt)))
	binary.LittleEndian.PutUint16(data[0:], 0)
	binary.LittleEndian.PutUint16(data[2:], uint16(2*len(sub))) //nolint:gosec // the test's short names
	binary.LittleEndian.PutUint16(data[4:], uint16(2*len(sub))) //nolint:gosec // the test's short names
	binary.LittleEndian.PutUint16(data[6:], uint16(2*len(prt))) //nolint:gosec // the test's short names

	for i, u := range append(sub, prt...) {
		binary.LittleEndian.PutUint16(data[header+2*i:], u)
	}

	return withHeader(tag, data)
}

func withHeader(tag uint32, data []byte) []byte {
	b := make([]byte, 8+len(data))
	binary.LittleEndian.PutUint32(b[0:], tag)
	binary.LittleEndian.PutUint16(b[4:], uint16(len(data))) //nolint:gosec // the test's short data
	copy(b[8:], data)

	return b
}

func TestLinkTargetReadsWhereALinkPoints(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"a WSL symlink":                    {withHeader(tagLxSymlink, append([]byte{2, 0, 0, 0}, "../lib/x.so"...)), "../lib/x.so"},
		"a relative Windows symlink":       {windowsLink(tagSymlink, `..\x`, `..\x`), "../x"},
		"an absolute Windows symlink":      {windowsLink(tagSymlink, `\??\C:\work\x`, `C:\work\x`), "C:/work/x"},
		"a symlink to a share":             {windowsLink(tagSymlink, `\??\UNC\server\share`, `\\server\share`), "//server/share"},
		"a junction without a print name":  {windowsLink(tagMountPoint, `\??\D:\src`, ""), "D:/src"},
		"a junction with a different name": {windowsLink(tagMountPoint, `\??\D:\src`, "printed"), "D:/src"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			got, ok := linkTarget(c.data)

			// assert
			assert.True(t, ok)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestLinkTargetRefusesReparseDataThatDoesNotFit(t *testing.T) {
	t.Parallel()

	junction := windowsLink(tagMountPoint, `\??\D:\src`, "")

	tooLong := append([]byte(nil), junction...)
	binary.LittleEndian.PutUint16(tooLong[10:], 0xFFFF)

	pastEnd := append([]byte(nil), junction...)
	binary.LittleEndian.PutUint16(pastEnd[4:], 0xFFFF)

	for name, data := range map[string][]byte{
		"nothing":                 nil,
		"a header only":           {0x1D, 0, 0, 0xA0},
		"a length past the data":  pastEnd,
		"a name past the buffer":  tooLong,
		"an empty WSL symlink":    withHeader(tagLxSymlink, []byte{2, 0, 0, 0}),
		"a tag that is no link":   withHeader(0x80000023, nil),
		"a junction without name": withHeader(tagMountPoint, make([]byte, 8)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			_, ok := linkTarget(data)

			// assert
			assert.False(t, ok)
		})
	}
}

func TestLinkTargetRefusesAWSLTargetWithANUL(t *testing.T) {
	t.Parallel()

	// act
	_, ok := linkTarget(withHeader(tagLxSymlink, append([]byte{2, 0, 0, 0}, "a\x00b"...)))

	// assert
	assert.False(t, ok)
}
