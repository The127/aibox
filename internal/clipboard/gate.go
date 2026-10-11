package clipboard

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// DefaultWindow is how long a press of the paste key lets the VM read the
// clipboard. Claude Code asks right after the key, twice: which types the
// clipboard holds, then the image. The window leaves room for a slow VM.
const DefaultWindow = 5 * time.Second

// The paths the VM asks for.
const (
	TypesPath = "/types"
	ImagePath = "/image.png"
)

// Gate serves the clipboard to the VM, but only after Arm. Asking for the
// types keeps it armed. Reading the image ends that, and so do a clipboard
// without an image and the end of the window.
type Gate struct {
	// Read returns the image on the clipboard as PNG
	Read func() ([]byte, error)
	// Window is how long an Arm lasts. Zero means DefaultWindow.
	Window time.Duration
	// Log gets why the VM got no image, or is nil
	Log io.Writer

	// reading keeps the VM to one request at a time
	reading sync.Mutex
	// mu guards the rest, which Arm changes while a read may run, so that
	// a slow read does not hold up the keys
	mu    sync.Mutex
	until time.Time
	// press counts the presses, so that a read only stores what it got
	// for the press it was made for
	press uint64
	// image is what the types were read from, so that the image the VM
	// then reads is the same
	image []byte
}

// Arm lets the VM read the clipboard once, within the window.
func (g *Gate) Arm() {
	window := g.Window
	if window == 0 {
		window = DefaultWindow
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.until = time.Now().Add(window)
	g.press++
	g.image = nil
}

// ServeHTTP answers TypesPath with the types on the clipboard and ImagePath
// with the image, while the gate is armed.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || (r.URL.Path != TypesPath && r.URL.Path != ImagePath) {
		http.NotFound(w, r)

		return
	}

	g.reading.Lock()
	defer g.reading.Unlock()

	g.mu.Lock()
	armed, press, image := time.Now().Before(g.until), g.press, g.image
	g.mu.Unlock()

	if !armed {
		http.Error(w, "the VM may read the clipboard only right after you press Ctrl+V", http.StatusForbidden)

		return
	}

	if image == nil {
		var err error
		if image, err = g.Read(); err != nil {
			g.logf("no image for the VM: %v", err)

			// the VM must not keep asking until an image shows up
			g.mu.Lock()
			if g.press == press {
				g.until = time.Time{}
			}

			g.mu.Unlock()

			status := http.StatusNotFound
			if !errors.Is(err, ErrNoImage) {
				status = http.StatusInternalServerError
			}

			http.Error(w, "the clipboard holds no image", status)

			return
		}
	}

	g.mu.Lock()
	if g.press == press {
		g.image = image

		if r.URL.Path == ImagePath {
			g.until, g.image = time.Time{}, nil
		}
	}

	g.mu.Unlock()

	if r.URL.Path == TypesPath {
		_, _ = io.WriteString(w, "image/png\n")

		return
	}

	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(image)
}

func (g *Gate) logf(format string, args ...any) {
	if g.Log != nil {
		_, _ = fmt.Fprintf(g.Log, time.Now().UTC().Format(time.RFC3339)+" clipboard: "+format+"\n", args...)
	}
}
