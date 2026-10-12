package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEditor records what it was asked to open. onEdit, when set, stands
// for what the person does in the editor.
type fakeEditor struct {
	command string
	path    string
	onEdit  func(path string) error
}

func (e *fakeEditor) edit(command, path string) error {
	e.command = command
	e.path = path

	if e.onEdit == nil {
		return nil
	}

	return e.onEdit(path)
}

func writes(content string) func(string) error {
	return func(path string) error { return os.WriteFile(path, []byte(content), 0o600) }
}

func (f *fixture) configEdit(args ...string) error {
	return newRootCommand(f.deps).Run(context.Background(), append([]string{"aibox", "config", "edit"}, args...))
}

func TestConfigEditOpensTheConfigOfTheProjectInTheEditor(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.lookupEnv = func(name string) (string, bool) {
		if name == "EDITOR" {
			return "nano", true
		}

		return "", false
	}

	// act
	err := f.configEdit()

	// assert
	require.NoError(t, err)
	assert.Equal(t, "nano", f.editor.command)
	assert.Equal(t, f.project(t).Config, f.editor.path)
}

func TestConfigEditPrefersVISUALLikeGit(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.lookupEnv = func(name string) (string, bool) {
		editor, ok := map[string]string{"VISUAL": "code --wait", "EDITOR": "nano"}[name]

		return editor, ok
	}

	// act
	err := f.configEdit()

	// assert
	require.NoError(t, err)
	assert.Equal(t, "code --wait", f.editor.command)
}

func TestConfigEditFallsBackToTheEditorOfTheSystem(t *testing.T) {
	// arrange
	f := newFixture(t)

	// act
	err := f.configEdit()

	// assert
	require.NoError(t, err)
	assert.Equal(t, fallbackEditor, f.editor.command)
}

func TestConfigEditTakesTheEditorFromTheFlagFirst(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.lookupEnv = func(string) (string, bool) { return "nano", true }

	// act
	err := f.configEdit("--editor", "hx")

	// assert
	require.NoError(t, err)
	assert.Equal(t, "hx", f.editor.command)
}

func TestConfigEditWritesTheDefaultFileForANewProject(t *testing.T) {
	// arrange
	f := newFixture(t)

	// act
	err := f.configEdit()

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(f.project(t).Config)
	require.NoError(t, err)
	assert.Contains(t, string(content), "api.anthropic.com")
}

func TestConfigEditOpensAFileThatDoesNotParse(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.writeConfig(t, "allow: [\n")
	f.editor.onEdit = writes("allow:\n  - example.com\n")

	// act
	err := f.configEdit()

	// assert
	require.NoError(t, err)
	assert.Equal(t, f.project(t).Config, f.editor.path)
}

func TestConfigEditReportsAFileThatDoesNotReadBack(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.editor.onEdit = writes("allow: [\n")

	// act
	err := f.configEdit()

	// assert
	require.ErrorContains(t, err, f.project(t).Config)
}

func TestConfigEditReportsAMountTheVMMustNotSee(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.editor.onEdit = writes("mounts:\n  - " + f.homeDir + ":/mnt/home\n")

	// act
	err := f.configEdit()

	// assert
	require.ErrorIs(t, err, errUnsafeMount)
	assert.ErrorContains(t, err, "home directory")
}

func TestConfigEditReportsAnEditorThatFailsAndSkipsTheReadBack(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.editor.onEdit = func(path string) error {
		_ = os.WriteFile(path, []byte("allow: [\n"), 0o600)

		return errors.New("exit status 1")
	}

	// act
	err := f.configEdit()

	// assert
	require.ErrorContains(t, err, "exit status 1")
	assert.NotContains(t, err.Error(), f.project(t).Config)
}

func TestRunEditorReportsAnEditorThatFails(t *testing.T) {
	// act
	err := runEditor("false", filepath.Join(t.TempDir(), "config.yaml"))

	// assert
	assert.Error(t, err)
}

func (f *fixture) writeTemplate(t *testing.T, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(f.aiboxDir, "config.yaml"), []byte(content), 0o600))
}

func TestANewProjectStartsFromTheTemplate(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.writeTemplate(t, "# my usual setup\nallow:\n  - api.anthropic.com\n  - preset:go\ncpus: 4\n")

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.NoError(t, err)
	assert.Equal(t, 4, f.launch.spec.CPUs)

	written, err := os.ReadFile(f.project(t).Config)
	require.NoError(t, err)
	assert.Equal(t, "# my usual setup\nallow:\n  - api.anthropic.com\n  - preset:go\ncpus: 4\n", string(written))

	// and keeps its config when the template changes
	f.writeTemplate(t, "cpus: 8\n")
	require.NoError(t, f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")))
	assert.Equal(t, 4, f.launch.spec.CPUs)
}

func TestConfigEditTemplateOpensTheTemplate(t *testing.T) {
	// arrange
	f := newFixture(t)
	template := filepath.Join(f.aiboxDir, "config.yaml")

	// act
	err := f.configEdit("--template")

	// assert
	require.NoError(t, err)
	assert.Equal(t, template, f.editor.path)
	assert.FileExists(t, template, "it starts from the default file")
	assert.NoDirExists(t, filepath.Join(f.aiboxDir, "projects"), "no project gets a folder for it")
}

func TestConfigEditTemplateReportsAMistake(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.editor.onEdit = writes("allow:\n  - preset:nothing\n")

	// act
	err := f.configEdit("--template")

	// assert
	require.ErrorContains(t, err, "unknown preset")
}

func TestConfigEditOfANewProjectStartsFromTheTemplate(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.writeTemplate(t, "memory: 4096\n")

	// act
	err := f.configEdit()

	// assert
	require.NoError(t, err)

	written, err := os.ReadFile(f.project(t).Config)
	require.NoError(t, err)
	assert.Equal(t, "memory: 4096\n", string(written))
}
