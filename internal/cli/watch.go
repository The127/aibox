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
	"syscall"
	"unicode"
	"unicode/utf8"
)

// watched are the files and folders of a project that programs on the host
// run without asking: git, direnv, lefthook, just, npm and the IDEs.
var watched = []string{
	".git/config",
	".git/hooks",
	".envrc",
	".husky",
	"lefthook.yml",
	"lefthook.yaml",
	".lefthook.yml",
	"justfile",
	"Justfile",
	".justfile",
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

// unreadable is the fingerprint of a file aibox could not read.
const unreadable = "unreadable"

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

	// in a linked worktree or a submodule, .git is a file that names the
	// folder of the repository
	if info, err := root.Lstat(".git"); err == nil && !info.IsDir() {
		shot.add(root, ".git")
	}

	for _, name := range distinct(root, paths) {
		shot.add(root, name)
	}

	return shot
}

// distinct are the paths without the ones that lead to a file an earlier
// one leads to, like justfile and Justfile where the file system tells no
// case apart. A path whose way leads through a link becomes that link,
// which os.Root would follow.
func distinct(root *os.Root, paths []string) []string {
	var names []string

	var files []fs.FileInfo

	for _, name := range paths {
		if link, ok := linkAbove(root, name); ok {
			name = link
		}

		info, err := root.Lstat(filepath.FromSlash(name))
		if err == nil {
			if slices.ContainsFunc(files, func(file fs.FileInfo) bool { return os.SameFile(file, info) }) {
				continue
			}

			files = append(files, info)
		}

		names = append(names, name)
	}

	return names
}

// linkAbove is the first folder on the way to the name that is a link.
func linkAbove(root *os.Root, name string) (string, bool) {
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		folder := path.Join(parts[:i]...)

		info, err := root.Lstat(filepath.FromSlash(folder))
		if err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return folder, true
		}
	}

	return "", false
}

func (s snapshot) add(root *os.Root, name string) {
	info, err := root.Lstat(filepath.FromSlash(name))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return
	}

	if err != nil {
		s[name] = unreadable

		return
	}

	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := root.Readlink(filepath.FromSlash(name))
		if err != nil {
			s[name] = unreadable

			return
		}

		s[name] = "link to " + target
	case info.IsDir():
		entries, err := readRootDir(root, filepath.FromSlash(name))
		if err != nil {
			s[name] = unreadable

			return
		}

		for _, entry := range entries {
			s.add(root, path.Join(name, entry.Name()))
		}
	case info.Mode().IsRegular():
		// the mode counts too: git runs a hook only once it may run
		s[name] = info.Mode().Perm().String() + " " + hashFile(root, name)
	default:
		s[name] = info.Mode().Type().String()
	}
}

func hashFile(root *os.Root, name string) string {
	file, err := openInRoot(root, filepath.FromSlash(name))
	if err != nil {
		return unreadable
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return unreadable
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
		case strings.HasSuffix(fingerprint, unreadable):
			changes[name] = unreadable
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
		line := fmt.Sprintf("  %-10s %s", changes[name], printable(name))
		if hint, ok := watchHints[name]; ok {
			line += " (" + hint + ")"
		}

		_, _ = fmt.Fprintln(w, line)
	}
}

// printable quotes a name the VM chose when it has characters a terminal
// would act on.
func printable(name string) string {
	if !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return strconv.Quote(name)
	}

	return name
}
