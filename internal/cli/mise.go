package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/the127/aibox/internal/project"
	"github.com/the127/aibox/internal/task"
)

// miseFiles are the files of the project that mise installs from in the
// VM, in the order they are hashed. The VM reads no other.
var miseFiles = []string{"mise.toml", ".tool-versions"}

// maxMiseBytes is the most aibox reads of a mise file. The VM can write the
// file, and aibox reads it whole and shows it on the terminal.
const maxMiseBytes = 1 << 20

var (
	errMiseLink  = errors.New("is no plain file")
	errMiseLarge = fmt.Errorf("is larger than %d bytes", maxMiseBytes)
)

// miseFile is a mise file of the project and what it holds.
type miseFile struct {
	name    string
	content []byte
}

// readMiseFiles reads the mise files in the folder of the project. A
// missing file is left out. The VM can put a link there, which aibox does
// not follow.
func readMiseFiles(dir string) ([]miseFile, error) {
	var files []miseFile

	for _, name := range miseFiles {
		path := filepath.Join(dir, name)

		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return nil, err
		}

		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s %w", name, errMiseLink)
		}

		content, err := readLimited(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		files = append(files, miseFile{name: name, content: content})
	}

	return files, nil
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // a file of the project
	if err != nil {
		return nil, err
	}

	defer func() { _ = f.Close() }()

	content, err := io.ReadAll(io.LimitReader(f, maxMiseBytes+1))
	if err != nil {
		return nil, err
	}

	if len(content) > maxMiseBytes {
		return nil, errMiseLarge
	}

	return content, nil
}

// readCommitMiseFiles reads the mise files of the commit, as the VM of a
// task checks it out. git runs in the helper and writes nothing.
func readCommitMiseFiles(dir, commit string) ([]miseFile, error) {
	var files []miseFile

	for _, name := range miseFiles {
		entry, err := gitOutput(dir, "", "ls-tree", "-l", "--end-of-options", commit, "--", name)
		if err != nil {
			return nil, err
		}

		if entry == "" {
			continue
		}

		// mode type object size, a tab and the name
		fields := strings.Fields(entry)
		if len(fields) < 4 || (fields[0] != "100644" && fields[0] != "100755") {
			return nil, fmt.Errorf("%s %w", name, errMiseLink)
		}

		if size, err := strconv.Atoi(fields[3]); err != nil || size > maxMiseBytes {
			return nil, fmt.Errorf("%s %w", name, errMiseLarge)
		}

		content, err := gitBytes(dir, "", "cat-file", "blob", fields[2])
		if err != nil {
			return nil, err
		}

		files = append(files, miseFile{name: name, content: content})
	}

	return files, nil
}

// hashMiseFiles is the hash the person approves: the names and the content
// of the files, each with its length, so that no two sets of files share
// one.
func hashMiseFiles(files []miseFile) string {
	h := sha256.New()

	for _, f := range files {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", f.name, len(f.content))
		_, _ = h.Write(f.content)
	}

	return hex.EncodeToString(h.Sum(nil))
}

// approvedMise says whether the person approved the files, by the hash
// aibox keeps for the project.
func approvedMise(p project.Project, files []miseFile) bool {
	approved, err := os.ReadFile(p.Mise)

	return err == nil && strings.TrimSpace(string(approved)) == hashMiseFiles(files)
}

// approveMise says whether the VM of aibox run installs the tools of the
// project. Files the person approved before go in without a question. New
// or changed ones are shown, and go in only when the person says yes,
// which aibox then keeps.
func approveMise(ctx context.Context, deps dependencies, dir string, p project.Project) (bool, error) {
	files, err := readMiseFiles(dir)
	if err != nil {
		return false, fmt.Errorf("mise: %w", err)
	}

	if len(files) == 0 {
		_, _ = fmt.Fprintf(deps.stderr, "aibox: mise is on, but the project has no %s\n", strings.Join(miseFiles, " or "))

		return false, nil
	}

	if approvedMise(p, files) {
		return true, nil
	}

	_, _ = fmt.Fprintf(deps.stderr, "aibox: mise installs the tools these files name, in the VM, before Claude Code starts:\n")

	for _, f := range files {
		_, _ = fmt.Fprintf(deps.stderr, "\n%s:\n%s\n", f.name, task.Indent(string(f.content), "  "))
	}

	_, _ = fmt.Fprintln(deps.stderr)

	err = confirm(ctx, deps, "Install them?", false)
	if errors.Is(err, errNotConfirmed) {
		_, _ = fmt.Fprintf(deps.stderr, "aibox: starting without them. aibox asks again next time.\n")

		return false, nil
	}

	if err != nil {
		return false, err
	}

	if err := os.WriteFile(p.Mise, []byte(hashMiseFiles(files)+"\n"), 0o600); err != nil {
		return false, fmt.Errorf("keep the approval of the mise files: %w", err)
	}

	return true, nil
}

// taskMise says whether the VM of a task installs the tools of the commit.
// A task can't ask, so only files the person approved in aibox run go in.
func taskMise(out io.Writer, dir, commit string, p project.Project) (bool, error) {
	files, err := readCommitMiseFiles(dir, commit)
	if err != nil {
		return false, fmt.Errorf("mise: %w", err)
	}

	switch {
	case len(files) == 0:
		_, _ = fmt.Fprintf(out, "mise is on, but the commit has no %s\n", strings.Join(miseFiles, " or "))

		return false, nil
	case !approvedMise(p, files):
		_, _ = fmt.Fprintf(out, "the task starts without the tools of mise: approve the mise files of the commit in aibox run first\n")

		return false, nil
	}

	return true, nil
}
