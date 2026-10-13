package clipboard

import "os"

// read gets the image as PNG from wl-paste under Wayland and from xclip
// under X, as Claude Code does on Linux.
func read() ([]byte, error) {
	name, args := "xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		name, args = "wl-paste", []string{"--no-newline", "--type", "image/png"}
	}

	return output(name, args...)
}

// readText gets the text as UTF-8 from wl-paste under Wayland and from
// xclip under X.
func readText() ([]byte, error) {
	name, args := "xclip", []string{"-selection", "clipboard", "-t", "UTF8_STRING", "-o"}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		name, args = "wl-paste", []string{"--no-newline", "--type", "text/plain;charset=utf-8"}
	}

	return output(name, args...)
}
