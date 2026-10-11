package clipboard

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
)

// errNotData is an answer of osascript that is not the data of an image.
var errNotData = errors.New("osascript answered no image data")

// read gets the image as PNG from osascript, as Claude Code does on macOS.
// osascript prints it as «data PNGf…» with the bytes in hex.
func read() ([]byte, error) {
	out, err := output("osascript", "-e", "the clipboard as «class PNGf»")
	if err != nil {
		return nil, err
	}

	return parseData(out)
}

func parseData(out []byte) ([]byte, error) {
	out = bytes.TrimSpace(out)

	digits, ok := bytes.CutPrefix(out, []byte("«data PNGf"))
	if !ok {
		return nil, errNotData
	}

	digits, ok = bytes.CutSuffix(digits, []byte("»"))
	if !ok {
		return nil, errNotData
	}

	data := make([]byte, hex.DecodedLen(len(digits)))
	if _, err := hex.Decode(data, digits); err != nil {
		return nil, fmt.Errorf("%w: %w", errNotData, err)
	}

	return data, nil
}
