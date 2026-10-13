package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestRunTellsWhichWatchedFilesTheVMChanged(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	writeFile(t, filepath.Join(f.cwd, ".git", "config"), "[core]\n")
	writeFile(t, filepath.Join(f.cwd, ".envrc"), "use flake\n")
	writeFile(t, filepath.Join(f.cwd, "README.md"), "hello\n")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		writeFile(t, filepath.Join(spec.Project, ".git", "config"), "[core]\n\tfsmonitor = ./evil\n")
		writeFile(t, filepath.Join(spec.Project, ".git", "hooks", "pre-commit"), "#!/bin/sh\n")
		require.NoError(t, os.Remove(filepath.Join(spec.Project, ".envrc")))
		writeFile(t, filepath.Join(spec.Project, "README.md"), "changed\n")

		return nil
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "aibox: the VM may have changed files that programs on this machine run. Read them before you run anything here:\n"+
		"  removed    .envrc\n"+
		"  changed    .git/config\n"+
		"  added      .git/hooks/pre-commit\n")
	assert.NotContains(t, f.stderr.String(), "README.md")
}

func TestRunTellsNothingWhenNoWatchedFileChanged(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	writeFile(t, filepath.Join(f.cwd, "justfile"), "test:\n")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		writeFile(t, filepath.Join(spec.Project, "main.go"), "package main\n")

		return nil
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.NotContains(t, f.stderr.String(), "may have changed")
}

func TestRunWatchesTheFilesOfTheConfigToo(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "allow: []\nwatch:\n  - Makefile\n  - .github/workflows\n")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		writeFile(t, filepath.Join(spec.Project, "Makefile"), "all:\n")
		writeFile(t, filepath.Join(spec.Project, ".github", "workflows", "ci.yml"), "on: push\n")

		return nil
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "  added      .github/workflows/ci.yml\n  added      Makefile\n")
}

func TestRunTellsWhenTheVMChangedWatchedFilesAndFailed(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		writeFile(t, filepath.Join(spec.Project, "lefthook.yml"), "pre-commit:\n")

		return context.Canceled
	}

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, f.stderr.String(), "  added      lefthook.yml\n")
}

func TestReportChangesSaysTheIDEChangesWorkspaceXMLToo(t *testing.T) {
	// arrange
	var out bytes.Buffer

	// act
	reportChanges(&out, snapshot{".idea/workspace.xml": "a"}, snapshot{".idea/workspace.xml": "b"})

	// assert
	assert.Contains(t, out.String(), "  changed    .idea/workspace.xml (your IDE changes it too)\n")
}

func TestReportChangesQuotesANameATerminalWouldActOn(t *testing.T) {
	// arrange
	var out bytes.Buffer

	// act
	reportChanges(&out, snapshot{}, snapshot{".vscode/\x1b[2Jtasks.json": "a"})

	// assert
	assert.Contains(t, out.String(), `  added      ".vscode/\x1b[2Jtasks.json"`+"\n")
}

func TestRunTellsWhenTheVMChangedTheGitFileOfAWorktree(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	writeFile(t, filepath.Join(f.cwd, ".git"), "gitdir: /repos/main/.git/worktrees/feature\n")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		writeFile(t, filepath.Join(spec.Project, ".git"), "gitdir: evil\n")

		return nil
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "  changed    .git\n")
}

func TestReportChangesQuotesANameThatIsNoUTF8(t *testing.T) {
	// arrange
	var out bytes.Buffer

	// act
	reportChanges(&out, snapshot{}, snapshot{".vscode/\x9b2J": "a"})

	// assert
	assert.Contains(t, out.String(), `  added      ".vscode/\x9b2J"`+"\n")
}
