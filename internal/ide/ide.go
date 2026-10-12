// Package ide finds the IDEs that Claude Code can connect to, from the lock
// files that their Claude Code plugins write, and links Claude Code in the
// VM to one of them.
package ide

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/the127/aibox/internal/realpath"
)

// maxLockBytes is more than a lock file ever needs, a few hundred bytes, so
// that a huge file in the folder cannot fill the memory.
const maxLockBytes = 64 << 10

// IDE is an IDE that serves Claude Code on a WebSocket on the loopback of
// the host. Token is what a client must show it. It must not get into the
// VM or into a log.
type IDE struct {
	Name  string
	Port  uint16
	Token string
}

// String names the IDE without its token.
func (i IDE) String() string {
	return fmt.Sprintf("%s on port %d", i.Name, i.Port)
}

type lock struct {
	WorkspaceFolders []string `json:"workspaceFolders"`
	IDEName          string   `json:"ideName"`
	Transport        string   `json:"transport"`
	AuthToken        string   `json:"authToken"`
}

type found struct {
	ide      IDE
	steps    int
	modified time.Time
}

// Find returns the IDEs of the lock files in dir that have the project open,
// best first: the one whose folder is closest to the project, then the one
// that wrote its lock file last. A file that is no lock file of a WebSocket
// is left out, and so is a missing dir. Whether an IDE still runs shows
// only when it is connected to, since the process a lock file names may be
// gone and its number taken by another.
func Find(dir, project string) ([]IDE, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read the lock files of the IDEs: %w", err)
	}

	project = realpath.Resolved(project)

	var all []found

	for _, entry := range entries {
		port, ok := portOf(entry.Name())
		if !ok || !entry.Type().IsRegular() {
			continue
		}

		f, folders, ok := read(filepath.Join(dir, entry.Name()))
		if !ok {
			continue
		}

		f.ide.Port = port
		if f.steps = closest(folders, project); f.steps >= 0 {
			all = append(all, f)
		}
	}

	slices.SortFunc(all, func(a, b found) int {
		return cmp.Or(
			cmp.Compare(a.steps, b.steps),
			b.modified.Compare(a.modified),
			cmp.Compare(b.ide.Port, a.ide.Port),
		)
	})

	ides := make([]IDE, len(all))
	for i, f := range all {
		ides[i] = f.ide
	}

	return ides, nil
}

// portOf reads the port from a name like 23214.lock, written the way a
// number is written, without a sign or leading zeros.
func portOf(name string) (uint16, bool) {
	digits, ok := strings.CutSuffix(name, ".lock")
	if !ok {
		return 0, false
	}

	port, err := strconv.ParseUint(digits, 10, 16)
	if err != nil || port == 0 || strconv.FormatUint(port, 10) != digits {
		return 0, false
	}

	return uint16(port), true
}

// read reads a lock file and the folders it names. A file that is too
// big, no JSON or no lock of a WebSocket is no lock file.
func read(path string) (found, []string, bool) {
	f, err := openLock(path)
	if err != nil {
		return found{}, nil, false
	}

	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return found{}, nil, false
	}

	data, err := io.ReadAll(io.LimitReader(f, maxLockBytes+1))
	if err != nil || len(data) > maxLockBytes {
		return found{}, nil, false
	}

	var l lock
	if err := json.Unmarshal(data, &l); err != nil || l.Transport != "ws" || l.AuthToken == "" {
		return found{}, nil, false
	}

	return found{ide: IDE{Name: l.IDEName, Token: l.AuthToken}, modified: info.ModTime()}, l.WorkspaceFolders, true
}

// closest is how many folders up from the project the closest of the
// folders is that holds it, or -1 when none does. Folders are compared as
// files, since a folder has more names than one: a symlink, or another
// case where the file system tells no case apart.
func closest(folders []string, project string) int {
	best := -1

	for _, folder := range folders {
		if !filepath.IsAbs(folder) {
			continue
		}

		want, err := os.Stat(folder)
		if err != nil {
			continue
		}

		path := project
		for steps := 0; best < 0 || steps < best; steps++ {
			if info, err := os.Stat(path); err == nil && os.SameFile(info, want) {
				best = steps

				break
			}

			parent := filepath.Dir(path)
			if parent == path {
				break
			}

			path = parent
		}
	}

	return best
}
