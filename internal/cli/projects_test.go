package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/project"
)

// projectsFixture is a project that ran before: a config, a home with a
// login and more, a state disk, logs and a task.
type projectsFixture struct {
	*fixture
	p project.Project
}

func newProjectsFixture(t *testing.T) *projectsFixture {
	t.Helper()

	f := &projectsFixture{fixture: newFixture(t)}
	f.p = f.project(t)

	for _, name := range []string{
		f.p.Config,
		f.p.State,
		f.p.Log,
		filepath.Join(f.p.Home, ".claude", credentialsFile),
		filepath.Join(f.p.Home, ".claude", "settings.json"),
		filepath.Join(f.p.Home, ".claude.json"),
		filepath.Join(f.p.Home, ".local", "bin", "tool"),
		filepath.Join(f.p.Dir, "tasks", "20261012-100000-abcdef", "result.json"),
	} {
		f.write(t, name)
	}

	return f
}

func (f *projectsFixture) write(t *testing.T, path string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
}

func (f *projectsFixture) projects(answer string, args ...string) error {
	f.deps.stdin = strings.NewReader(answer)

	return newRootCommand(f.deps).Run(context.Background(), append([]string{"aibox", "projects"}, args...))
}

// running holds the lock aibox run holds, until the test ends.
func (f *projectsFixture) running(t *testing.T) {
	t.Helper()

	lock, err := lockRunOf(f.p.Dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
}

func TestResetClearsTheVMButKeepsTheLogin(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)

	// act
	err := f.projects("y\n", "reset")

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, f.p.State)
	assert.NoFileExists(t, filepath.Join(f.p.Home, ".claude.json"))
	assert.NoFileExists(t, filepath.Join(f.p.Home, ".claude", "settings.json"))
	assert.NoDirExists(t, filepath.Join(f.p.Home, ".local"))
	assert.FileExists(t, filepath.Join(f.p.Home, ".claude", credentialsFile))
	assert.FileExists(t, f.p.Config)
	assert.FileExists(t, f.p.Log)
	assert.FileExists(t, filepath.Join(f.p.Dir, "tasks", "20261012-100000-abcdef", "result.json"))
	assert.Contains(t, f.stderr.String(), "This clears its home folder, apart from the login, and its state disk. [y/N]")
}

func TestResetLogoutClearsTheLoginToo(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)

	// act
	err := f.projects("yes\n", "reset", "--logout")

	// assert
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(f.p.Home, ".claude"))
	assert.DirExists(t, f.p.Home)
	assert.NoFileExists(t, f.p.State)
	assert.FileExists(t, f.p.Config)
}

func TestResetKeepsNoLoginThatIsALink(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)
	outside := filepath.Join(t.TempDir(), "credentials")
	f.write(t, outside)

	login := filepath.Join(f.p.Home, ".claude", credentialsFile)
	require.NoError(t, os.Remove(login))
	symlink(t, outside, login)

	// act
	err := f.projects("y\n", "reset")

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, login)
	assert.FileExists(t, outside, "the reset follows no link out of the home")
}

func TestResetKeepsNoClaudeFolderThatIsALink(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)
	outside := t.TempDir()
	f.write(t, filepath.Join(outside, credentialsFile))
	f.write(t, filepath.Join(outside, "other"))

	claude := filepath.Join(f.p.Home, ".claude")
	require.NoError(t, os.RemoveAll(claude))
	symlink(t, outside, claude)

	// act
	err := f.projects("y\n", "reset")

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, claude)
	assert.FileExists(t, filepath.Join(outside, credentialsFile), "the reset follows no link out of the home")
	assert.FileExists(t, filepath.Join(outside, "other"))
}

func TestResetChangesNothingUnlessConfirmed(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)

	// act
	err := f.projects("n\n", "reset")

	// assert
	require.ErrorIs(t, err, errNotConfirmed)
	assert.FileExists(t, f.p.State)
	assert.FileExists(t, filepath.Join(f.p.Home, ".claude.json"))
}

func TestResetWithoutATerminalNeedsYes(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)
	f.deps.stdinIsTerminal = func() bool { return false }

	// act
	err := f.projects("y\n", "reset")

	// assert
	require.ErrorIs(t, err, errNeedsYes)
	assert.FileExists(t, f.p.State)

	// and goes ahead with it
	require.NoError(t, f.projects("", "reset", "--yes"))
	assert.NoFileExists(t, f.p.State)
}

func TestResetRefusesAProjectThatRuns(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)
	f.running(t)

	// act
	err := f.projects("y\n", "reset")

	// assert
	require.ErrorIs(t, err, errProjectRuns)
	assert.FileExists(t, f.p.State)
	assert.FileExists(t, filepath.Join(f.p.Home, ".claude.json"))
}

func TestResetOfAProjectAiboxKeepsNothingFor(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)
	f.cwd = t.TempDir()

	// act
	err := f.projects("y\n", "reset")

	// assert
	require.ErrorIs(t, err, errNoProject)
}

func TestRemoveRemovesEverything(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)

	// act
	err := f.projects("y\n", "remove")

	// assert
	require.NoError(t, err)
	assert.NoDirExists(t, f.p.Dir)
	assert.DirExists(t, f.cwd, "the project itself stays")
}

func TestRemoveTakesThePathOfAFolderThatIsGone(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)
	gone := f.cwd
	f.cwd = t.TempDir()
	require.NoError(t, os.RemoveAll(gone))

	// act
	err := f.projects("y\n", "remove", gone)

	// assert
	require.NoError(t, err)
	assert.NoDirExists(t, f.p.Dir)
}

func TestRemoveRefusesAProjectThatRuns(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)
	f.running(t)

	// act
	err := f.projects("y\n", "remove")

	// assert
	require.ErrorIs(t, err, errProjectRuns)
	assert.FileExists(t, f.p.Config)
}

func TestRemoveRefusesAProjectWithATaskThatRuns(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)
	lock, err := lockTasksOf(f.p.Dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })

	// act
	err = f.projects("y\n", "remove")

	// assert
	require.ErrorIs(t, err, errTaskRuns)
	assert.FileExists(t, f.p.Config)
	assert.FileExists(t, filepath.Join(f.p.Dir, "tasks", "20261012-100000-abcdef", "result.json"))

	// and a reset leaves it be, as the task has a disk and a home of its own
	require.NoError(t, f.projects("y\n", "reset"))
	assert.NoFileExists(t, f.p.State)
}

func TestTasksRunSideBySideButNotBesideARemove(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)

	first, err := lockTasksOf(f.p.Dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	// act
	second, err := lockTasksOf(f.p.Dir)

	// assert
	require.NoError(t, err)
	require.NoError(t, second.Close())

	require.NoError(t, first.Close())
	remove, err := lockProject(f.p.Dir, tasksLockFile, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = remove.Close() })

	_, err = lockTasksOf(f.p.Dir)
	assert.ErrorIs(t, err, errProjectBusy)
}

func TestRunRefusesAProjectThatAnotherAiboxHolds(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)
	f.running(t)
	f.version = "0.2.0"
	f.digests = map[string]string{}

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errProjectBusy)
	assert.False(t, f.launch.called)
}

func TestListShowsThePathsOfTheProjects(t *testing.T) {
	// arrange
	f := newProjectsFixture(t)

	gone := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, os.Mkdir(gone, 0o700))
	_, err := project.Open(filepath.Join(f.aiboxDir, "projects"), gone)
	require.NoError(t, err)
	require.NoError(t, os.Remove(gone))

	old := project.In(filepath.Join(f.aiboxDir, "projects", "-from-an-older-aibox"))
	f.write(t, old.Config)

	// act
	err = f.projects("", "list")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stdout.String(), filepath.Clean(f.cwd)+"\n")
	assert.Contains(t, f.stdout.String(), gone+" (the folder is gone)\n")
	assert.Contains(t, f.stdout.String(), "-from-an-older-aibox (folder "+old.Dir+", its path is known after its next run)\n")
}
