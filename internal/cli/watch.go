package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// watched are the files and folders of a project that programs on the host
// run without asking: git, direnv, lefthook, just, npm and the IDEs.
var watched = []string{
	".git/config",
	".git/hooks",
	".envrc",
	"lefthook.yml",
	"justfile",
	"package.json",
	".idea",
	".vscode",
}

// watchHints are said beside a changed file that the host changes on its
// own as well.
var watchHints = map[string]string{
	".idea/workspace.xml": "your IDE changes it too",
}

// snapshot is a fingerprint of each file under the watched paths of the
// project, by its path with slashes.
type snapshot map[string]string

// takeSnapshot fingerprints the files under the paths in the project. The
// VM writes the project, so it reads no link, waits for no FIFO and never
// leaves the project.
func takeSnapshot(project string, paths []string) snapshot {
	shot := snapshot{}

	root, err := os.OpenRoot(project)
	if err != nil {
		return shot
	}

	defer func() { _ = root.Close() }()

	for _, name := range paths {
		shot.add(root, name)
	}

	return shot
}

func (s snapshot) add(root *os.Root, name string) {
	info, err := root.Lstat(filepath.FromSlash(name))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}

	if err != nil {
		s[name] = "unreadable"

		return
	}

	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := root.Readlink(filepath.FromSlash(name))
		if err != nil {
			s[name] = "unreadable"

			return
		}

		s[name] = "link to " + target
	case info.IsDir():
		entries, err := readRootDir(root, filepath.FromSlash(name))
		if err != nil {
			s[name] = "unreadable"

			return
		}

		for _, entry := range entries {
			s.add(root, path.Join(name, entry.Name()))
		}
	case info.Mode().IsRegular():
		s[name] = hashFile(root, name)
	default:
		s[name] = info.Mode().Type().String()
	}
}

func hashFile(root *os.Root, name string) string {
	file, err := openInRoot(root, filepath.FromSlash(name))
	if err != nil {
		return "unreadable"
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "unreadable"
	}

	return hex.EncodeToString(hash.Sum(nil))
}

// reportChanges tells which files under the watched paths were added,
// changed or removed from before to after.
func reportChanges(w io.Writer, before, after snapshot) {
	changes := map[string]string{}

	for name, fingerprint := range after {
		old, ok := before[name]

		switch {
		case !ok:
			changes[name] = "added"
		case old != fingerprint:
			changes[name] = "changed"
		}
	}

	for name := range before {
		if _, ok := after[name]; !ok {
			changes[name] = "removed"
		}
	}

	if len(changes) == 0 {
		return
	}

	_, _ = fmt.Fprintln(w, "aibox: the VM may have changed files that programs on this machine run. Read them before you run anything here:")

	for _, name := range slices.Sorted(maps.Keys(changes)) {
		line := fmt.Sprintf("  %-8s %s", changes[name], printable(name))
		if hint, ok := watchHints[name]; ok {
			line += " (" + hint + ")"
		}

		_, _ = fmt.Fprintln(w, line)
	}
}

// printable quotes a name the VM chose when it has characters a terminal
// would act on.
func printable(name string) string {
	if strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return strconv.Quote(name)
	}

	return name
}
