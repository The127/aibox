//go:build !windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
)

func TestRunTellsOfALinkAndAFIFOTheVMPutInPlaceOfWatchedFiles(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	writeFile(t, filepath.Join(f.cwd, ".envrc"), "use flake\n")
	writeFile(t, filepath.Join(f.cwd, "package.json"), "{}\n")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		envrc := filepath.Join(spec.Project, ".envrc")
		require.NoError(t, os.Remove(envrc))
		require.NoError(t, os.Symlink("/etc/passwd", envrc))

		pkg := filepath.Join(spec.Project, "package.json")
		require.NoError(t, os.Remove(pkg))
		require.NoError(t, syscall.Mkfifo(pkg, 0o600))

		return nil
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "  changed    .envrc\n  changed    package.json\n")
}

func TestRunTellsWhenTheVMMadeAHookRunnable(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	writeFile(t, filepath.Join(f.cwd, ".git", "hooks", "pre-commit"), "#!/bin/sh\n")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		return os.Chmod(filepath.Join(spec.Project, ".git", "hooks", "pre-commit"), 0o700) //nolint:gosec // git runs only a hook it may run
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "  changed    .git/hooks/pre-commit\n")
}

func TestRunTellsOfAWatchedFileItCannotRead(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		writeFile(t, filepath.Join(spec.Project, ".envrc"), "use flake\n")

		return os.Chmod(filepath.Join(spec.Project, ".envrc"), 0o000)
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "  unreadable .envrc\n")
}

func TestRunTellsOfALinkOnTheWayToAWatchedFolder(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "allow: []\nwatch:\n  - .github/workflows\n")
	writeFile(t, filepath.Join(f.cwd, ".github", "workflows", "ci.yml"), "on: push\n")
	writeFile(t, filepath.Join(f.cwd, "src", "workflows", "ci.yml"), "on: push\n")
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		require.NoError(t, os.RemoveAll(filepath.Join(spec.Project, ".github")))

		return os.Symlink("src", filepath.Join(spec.Project, ".github"))
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "  added      .github\n  removed    .github/workflows/ci.yml\n")
}
