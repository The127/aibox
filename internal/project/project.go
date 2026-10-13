// Package project manages the folders aibox keeps for each project.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrRelativePath is returned for a project path that is not absolute.
var ErrRelativePath = errors.New("project path is not absolute")

// Project is the folder aibox keeps for one project. Dir is that folder.
// Home is the home folder of the VM, Config the config file, Log the log of
// the proxy, ConsoleLog the console of the last run of the VM and
// State the disk the VM keeps its installed tools and caches on, and Mise
// the hash of the mise files of the project the person approved. Home is
// shared into the VM and State is its second disk. Path names the file
// that holds the path of the project, so that a list of the projects can
// show it, which the name of Dir doesn't give back.
type Project struct {
	Dir        string
	Home       string
	Config     string
	Log        string
	ConsoleLog string
	State      string
	Mise       string
	Path       string
}

// maxName is the longest name a folder may have on Linux, macOS and
// Windows, in bytes.
const maxName = 255

// Escape turns a project path into a folder name. Letters and digits stay,
// a separator becomes -, and every other byte becomes _ and its two hex
// digits, _ too. So - and _ never stand for themselves, and no two paths
// get the same name. A name that would be too long for a folder is cut,
// and ends in _h and the SHA-256 of the path instead, which no escape
// makes.
func Escape(path string) string {
	pieces := make([]string, 0, len(path))
	length := 0

	for _, b := range []byte(path) {
		var piece string

		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
			piece = string(b)
		case b == filepath.Separator:
			piece = "-"
		default:
			piece = fmt.Sprintf("_%02x", b)
		}

		pieces = append(pieces, piece)
		length += len(piece)
	}

	if length <= maxName {
		return strings.Join(pieces, "")
	}

	sum := sha256.Sum256([]byte(path))
	tail := "_h" + hex.EncodeToString(sum[:])

	var name strings.Builder

	for _, piece := range pieces {
		if name.Len()+len(piece)+len(tail) > maxName {
			break
		}

		name.WriteString(piece)
	}

	return name.String() + tail
}

// At returns the folders for the project at path below base, without
// creating them.
func At(base, path string) (Project, error) {
	if !filepath.IsAbs(path) {
		return Project{}, fmt.Errorf("%w: %s", ErrRelativePath, path)
	}

	return In(filepath.Join(base, Escape(filepath.Clean(path)))), nil
}

// In returns the folders of the project whose folder is dir.
func In(dir string) Project {
	return Project{
		Dir:        dir,
		Home:       filepath.Join(dir, "home"),
		Config:     filepath.Join(dir, "config.yaml"),
		Log:        filepath.Join(dir, "proxy.log"),
		ConsoleLog: filepath.Join(dir, "console.log"),
		State:      filepath.Join(dir, "state.ext4"),
		Mise:       filepath.Join(dir, "mise.sha256"),
		Path:       filepath.Join(dir, "path"),
	}
}

// Open returns the folders for the project at path below base, creates
// them if they are missing and writes the path into its file.
func Open(base, path string) (Project, error) {
	p, err := At(base, path)
	if err != nil {
		return Project{}, err
	}

	// the home folder holds the login of Claude Code
	if err := os.MkdirAll(p.Home, 0o700); err != nil {
		return Project{}, fmt.Errorf("create %s: %w", p.Home, err)
	}

	if err := writePath(p.Path, filepath.Clean(path)+"\n"); err != nil {
		return Project{}, fmt.Errorf("write %s: %w", p.Path, err)
	}

	return p, nil
}

// writePath writes the path into its file, unless it holds it already. It
// replaces the file in one step, so that a list never reads it half
// written. A run and its tasks may write it at once, so each writes a file
// of its own first.
func writePath(file, path string) error {
	if written, err := os.ReadFile(file); err == nil && string(written) == path { //nolint:gosec // the path file of the project
		return nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+"-*")
	if err != nil {
		return err
	}

	_, err = tmp.WriteString(path)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}

	if err != nil {
		_ = os.Remove(tmp.Name())
	}

	return err
}
