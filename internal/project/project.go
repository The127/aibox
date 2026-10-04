// Package project manages the folders aibox keeps for each project.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// ErrRelativePath is returned for a project path that is not absolute.
var ErrRelativePath = errors.New("project path is not absolute")

var notAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9]`)

// Project is the folder aibox keeps for one project. Dir is that folder.
// Home is the home folder of the VM, Config the config file, Log the log of
// refused hosts, ConsoleLog the console of the last run of the VM and
// State the disk the VM keeps its installed tools and caches on. Home is
// shared into the VM and State is its second disk.
type Project struct {
	Dir        string
	Home       string
	Config     string
	Log        string
	ConsoleLog string
	State      string
}

// Escape turns a project path into a folder name.
func Escape(path string) string {
	return notAlphanumeric.ReplaceAllString(path, "-")
}

// Open returns the folders for the project at path below base and creates
// them if they are missing.
func Open(base, path string) (Project, error) {
	if !filepath.IsAbs(path) {
		return Project{}, fmt.Errorf("%w: %s", ErrRelativePath, path)
	}

	dir := filepath.Join(base, Escape(filepath.Clean(path)))
	p := Project{
		Dir:        dir,
		Home:       filepath.Join(dir, "home"),
		Config:     filepath.Join(dir, "config.yaml"),
		Log:        filepath.Join(dir, "proxy.log"),
		ConsoleLog: filepath.Join(dir, "console.log"),
		State:      filepath.Join(dir, "state.ext4"),
	}

	// the home folder holds the login of Claude Code
	if err := os.MkdirAll(p.Home, 0o700); err != nil {
		return Project{}, fmt.Errorf("create %s: %w", p.Home, err)
	}

	return p, nil
}

// CreateState makes the state disk at path with the size, as a sparse file
// that takes up space only as the VM writes to it. An existing disk keeps
// its size, so the size counts on the first run only. An empty file is
// sized again, since a run stopped between creating and sizing leaves one.
func CreateState(path string, size int64) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600) //nolint:gosec // the path is the project's state disk
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("look at %s: %w", path, err)
	}

	if info.Size() != 0 {
		return nil
	}

	if err := file.Truncate(size); err != nil {
		return fmt.Errorf("size %s: %w", path, err)
	}

	return nil
}
