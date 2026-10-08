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
// the proxy, ConsoleLog the console of the last run of the VM and
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
