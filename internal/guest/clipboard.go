//go:build linux

package guest

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/the127/aibox/internal/vm"
)

// The names the init answers to as the clipboard tools of the VM, which
// Claude Code runs when the person presses Ctrl+V.
const (
	xclip   = "xclip"
	wlPaste = "wl-paste"
)

// The paths of the clipboard on the host, as the host serves them.
const (
	clipboardTypes = "/types"
	clipboardImage = "/image.png"
)

// clipboardTimeout is how long the host may take to read its clipboard.
const clipboardTimeout = 30 * time.Second

// IsClipboardTool tells whether the init was started as one of the
// clipboard tools of the VM, by the name in args[0].
func IsClipboardTool(args []string) bool {
	return len(args) > 0 && slices.Contains([]string{xclip, wlPaste}, filepath.Base(args[0]))
}

// ClipboardTool runs the init as xclip or wl-paste: it asks the host for
// what Claude Code asks of these tools, the types on the clipboard or the
// image as PNG, and returns the exit code.
func ClipboardTool(args []string, stdout, stderr io.Writer) int {
	base := "http://127.0.0.1:" + strconv.Itoa(int(vm.ClipboardPort))

	return clipboardTool(base, args, stdout, stderr)
}

func clipboardTool(base string, args []string, stdout, stderr io.Writer) int {
	name := filepath.Base(args[0])

	path, ok := clipboardPath(name, args[1:])
	if !ok {
		_, _ = fmt.Fprintf(stderr, "%s: in aibox it only gives Claude Code an image from the clipboard of the host when you press Ctrl+V\n", name)

		return 1
	}

	client := http.Client{Timeout: clipboardTimeout}

	response, err := client.Get(base + path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", name, err)

		return 1
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, maxReplyBytes))
		_, _ = fmt.Fprintf(stderr, "%s: %s\n", name, bytes.TrimSpace(message))

		return 1
	}

	if _, err := io.Copy(stdout, response.Body); err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", name, err)

		return 1
	}

	return 0
}

// clipboardPath is the path on the host for what the arguments ask, in the
// forms Claude Code uses: xclip -selection clipboard -t TARGETS -o or -t
// image/png -o, and wl-paste -l or --type image/png.
func clipboardPath(name string, args []string) (string, bool) {
	var selection, target string

	output, list := false, false

	for i := 0; i < len(args); i++ {
		value := func() string {
			if i+1 < len(args) {
				i++

				return args[i]
			}

			return ""
		}

		switch args[i] {
		case "-selection":
			selection = value()
		case "-t", "--type":
			target = value()
		case "-o":
			output = true
		case "-l", "--list-types":
			list = true
		case "-n", "--no-newline":
		default:
			return "", false
		}
	}

	switch {
	case name == xclip && output && selection == "clipboard" && target == "TARGETS":
		return clipboardTypes, true
	case name == xclip && output && selection == "clipboard" && target == "image/png":
		return clipboardImage, true
	case name == wlPaste && list && target == "" && selection == "":
		return clipboardTypes, true
	case name == wlPaste && !list && target == "image/png" && selection == "":
		return clipboardImage, true
	default:
		return "", false
	}
}
