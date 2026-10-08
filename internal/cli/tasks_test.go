package cli

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/task"
)

// tasksFixture is a project with the folders of tasks, as aibox task leaves
// them.
type tasksFixture struct {
	*taskFixture
	tasks string
}

func newTasksFixture(t *testing.T) *tasksFixture {
	t.Helper()

	f := &tasksFixture{taskFixture: newTaskFixture(t)}
	f.tasks = filepath.Join(f.project(t).Dir, "tasks")

	return f
}

func (f *tasksFixture) write(t *testing.T, name string) {
	t.Helper()

	path := filepath.Join(f.tasks, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
}

// ended makes the folder of a task that ended.
func (f *tasksFixture) ended(t *testing.T, name string) {
	t.Helper()

	f.write(t, filepath.Join(name, lockFile))
	f.write(t, filepath.Join(name, "share", task.InputBundle))
	f.write(t, filepath.Join(name, "share", task.PromptFile))
}

// running makes the folder of a task that runs, until the test ends.
func (f *tasksFixture) running(t *testing.T, name string) {
	t.Helper()

	f.ended(t, name)

	lock, err := lockTask(filepath.Join(f.tasks, name), 0, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
}

func (f *tasksFixture) age(t *testing.T, name string, age time.Duration) {
	t.Helper()

	old := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(filepath.Join(f.tasks, name), old, old))
}

func (f *tasksFixture) clean(args ...string) error {
	return newRootCommand(f.deps).Run(context.Background(), append([]string{"aibox", "tasks", "clean"}, args...))
}

func TestCleanRemovesTheInputsOfTasksThatEnded(t *testing.T) {
	// arrange
	f := newTasksFixture(t)
	f.ended(t, "ended")
	f.write(t, filepath.Join("older", "share", task.InputBundle))
	f.running(t, "running")
	f.ended(t, "linked")
	require.NoError(t, os.RemoveAll(filepath.Join(f.tasks, "linked", "share")))
	f.write(t, filepath.Join("elsewhere", task.InputBundle))
	require.NoError(t, os.Symlink(filepath.Join(f.tasks, "elsewhere"), filepath.Join(f.tasks, "linked", "share")))
	f.write(t, filepath.Join("fifo", "share", task.InputBundle))
	require.NoError(t, syscall.Mkfifo(filepath.Join(f.tasks, "fifo", lockFile), 0o600))

	// act
	err := f.clean()

	// assert
	require.ErrorContains(t, err, "check the task fifo")
	assert.NoFileExists(t, filepath.Join(f.tasks, "ended", "share", task.InputBundle))
	assert.FileExists(t, filepath.Join(f.tasks, "ended", "share", task.PromptFile))
	assert.NoFileExists(t, filepath.Join(f.tasks, "older", "share", task.InputBundle))
	assert.FileExists(t, filepath.Join(f.tasks, "running", "share", task.InputBundle))
	assert.FileExists(t, filepath.Join(f.tasks, "elsewhere", task.InputBundle))
	assert.FileExists(t, filepath.Join(f.tasks, "fifo", "share", task.InputBundle))
	assert.Contains(t, f.stderr.String(), "aibox: removed the inputs of 2 tasks that ended\n")
	assert.Contains(t, f.stderr.String(), "aibox: 1 task still runs and stays as it is\n")
}

func TestCleanAllRemovesTheFoldersOfTasksThatEnded(t *testing.T) {
	// arrange
	f := newTasksFixture(t)
	f.ended(t, "20200101-000000-aaaaaa")
	f.ended(t, time.Now().Format(taskIDTime)+"-bbbbbb")
	f.running(t, "20200101-000000-cccccc")

	// act
	err := f.clean("--all", "--older-than", "720h")

	// assert
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(f.tasks, "20200101-000000-aaaaaa"))
	assert.DirExists(t, filepath.Join(f.tasks, time.Now().Format(taskIDTime)+"-bbbbbb"))
	assert.DirExists(t, filepath.Join(f.tasks, "20200101-000000-cccccc"))
	assert.Contains(t, f.stderr.String(), "aibox: removed the folders of 1 task that ended\n")
}

func TestCleanRemovesFoldersOfNewTasksOnlyWhenTheyAreOld(t *testing.T) {
	// arrange
	f := newTasksFixture(t)
	f.write(t, filepath.Join(".new-stale", "share", task.PromptFile))
	f.write(t, filepath.Join(".new-fresh", "share", task.PromptFile))
	f.write(t, filepath.Join(".new-unlocked", lockFile))
	f.write(t, filepath.Join(".new-fresh-unlocked", lockFile))
	f.running(t, ".new-locked")

	for _, name := range []string{".new-stale", ".new-unlocked", ".new-locked"} {
		f.age(t, name, time.Hour)
	}

	// act
	err := f.clean()

	// assert
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(f.tasks, ".new-stale"))
	assert.DirExists(t, filepath.Join(f.tasks, ".new-fresh"))
	assert.NoDirExists(t, filepath.Join(f.tasks, ".new-unlocked"))
	assert.DirExists(t, filepath.Join(f.tasks, ".new-fresh-unlocked"))
	assert.DirExists(t, filepath.Join(f.tasks, ".new-locked"))
	assert.Contains(t, f.stderr.String(), "aibox: removed the inputs of 0 tasks that ended\n")
	assert.Contains(t, f.stderr.String(), "aibox: 1 task still runs")
}

func TestCleanRemovesOnlyTheInputsOfOldTasks(t *testing.T) {
	// arrange
	f := newTasksFixture(t)
	f.ended(t, "20200101-000000-aaaaaa")
	recent := time.Now().Format(taskIDTime) + "-bbbbbb"
	f.ended(t, recent)

	// act
	err := f.clean("--older-than", "720h")

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(f.tasks, "20200101-000000-aaaaaa", "share", task.InputBundle))
	assert.FileExists(t, filepath.Join(f.tasks, recent, "share", task.InputBundle))
	assert.DirExists(t, filepath.Join(f.tasks, "20200101-000000-aaaaaa"))
}

func TestCleanRefusesANegativeAge(t *testing.T) {
	// act
	err := newTasksFixture(t).clean("--older-than", "-1h")

	// assert
	require.ErrorIs(t, err, errNegativeAge)
}

func TestCleanReportsWhatItCouldNotRemove(t *testing.T) {
	// arrange
	f := newTasksFixture(t)
	f.write(t, filepath.Join("stuck", lockFile))
	f.write(t, filepath.Join("stuck", "share", task.InputBundle, "file"))
	f.ended(t, "ended")

	// act
	err := f.clean()

	// assert
	require.ErrorContains(t, err, "remove the input of the task stuck")
	assert.NoFileExists(t, filepath.Join(f.tasks, "ended", "share", task.InputBundle))
	assert.Contains(t, f.stderr.String(), "aibox: removed the inputs of 1 task that ended\n")
}

func TestCleanWithoutTasksRemovesNothing(t *testing.T) {
	// arrange
	f := newTasksFixture(t)

	// act
	err := f.clean()

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "aibox: removed the inputs of 0 tasks that ended\n")
}

func TestATaskRemovesNoInputOfAnotherTask(t *testing.T) {
	// arrange
	f := newTasksFixture(t)
	f.ended(t, "20200101-000000-aaaaaa")
	f.age(t, "20200101-000000-aaaaaa", 24*time.Hour)
	f.succeeds(t)

	// act
	err := f.task("fix it")

	// assert
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(f.tasks, "20200101-000000-aaaaaa", "share", task.InputBundle))
}
