package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/filelock"
	"github.com/the127/aibox/internal/task"
)

// newTaskGrace is how long a folder of a new task may lack its lock, while
// another aibox makes it.
const newTaskGrace = time.Minute

func tasksCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:  "tasks",
		Usage: "the tasks of the project in the current folder",
		Commands: []*cli.Command{{
			Name:  "clean",
			Usage: "remove the bundles of the inputs of the tasks that ended, which hold the whole history of the project",
			Flags: []cli.Flag{
				&cli.BoolFlag{Name: "all", Usage: "remove the folders of the tasks that ended, with their results"},
				&cli.DurationFlag{Name: "older-than", Usage: "only the tasks that started longer ago, such as 720h", DefaultText: "every task that ended"},
			},
			Action: func(_ context.Context, cmd *cli.Command) error {
				return runClean(deps, cmd.Bool("all"), cmd.Duration("older-than"))
			},
		}},
	}
}

func runClean(deps dependencies, all bool, olderThan time.Duration) error {
	if olderThan < 0 {
		return errNegativeAge
	}

	cwd, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	p, err := openProject(aibox, cwd)
	if err != nil {
		return err
	}

	var before time.Time
	if olderThan > 0 {
		before = time.Now().Add(-olderThan)
	}

	result, err := cleanTasks(filepath.Join(p.Dir, "tasks"), all, before)

	what := "the inputs of"
	if all {
		what = "the folders of"
	}

	_, _ = fmt.Fprintf(deps.stderr, "aibox: removed %s %s that ended\n", what, count(result.cleaned, "task"))

	if result.running > 0 {
		_, _ = fmt.Fprintf(deps.stderr, "aibox: %s still %s and %s as %s\n", count(result.running, "task"), plural(result.running, "runs", "run"), plural(result.running, "stays", "stay"), plural(result.running, "it is", "they are"))
	}

	return err
}

// count is n and the noun, with an s unless n is 1.
func count(n int, noun string) string {
	return fmt.Sprintf("%d %s", n, plural(n, noun, noun+"s"))
}

func plural(n int, one, more string) string {
	if n == 1 {
		return one
	}

	return more
}

var errNegativeAge = errors.New("--older-than must not be negative")

// cleaned is what cleanTasks did: the tasks it cleaned and the tasks it
// left alone, since they run.
type cleaned struct {
	cleaned, running int
}

// cleanTasks removes the bundle of the input, or with all the whole folder,
// of every task in the folder of the tasks that ended and started before
// before. A zero before checks no age: every task that ended counts. A
// task that runs holds its lock, and its folder stays. A folder without a
// lock file counts as one of a task that ended: a task locks
// its lock before it makes its folder. cleanTasks also removes what is
// left of tasks that failed to start over a minute ago: a lock without
// its folder, and a folder an aibox from before the lock was beside the
// folder had not yet named. It returns an error for every folder it could
// not check or clean.
func cleanTasks(tasks string, all bool, before time.Time) (cleaned, error) {
	var result cleaned

	entries, err := os.ReadDir(tasks)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}

	if err != nil {
		return result, err
	}

	root, err := os.OpenRoot(tasks)
	if err != nil {
		return result, err
	}

	defer func() { _ = root.Close() }()

	var errs []error

	for _, entry := range entries {
		if !entry.IsDir() {
			if err := removeStrayLock(tasks, entry); err != nil {
				errs = append(errs, err)
			}

			continue
		}

		name := entry.Name()

		// one handle on the folder, so that the lock and what gets removed
		// are of the same folder
		// a folder that went away since, such as a new one that got its
		// name, is no error
		taskRoot, err := root.OpenRoot(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("open the task %s: %w", name, err))

			continue
		}

		lock, err := lockOf(tasks, taskRoot, name)

		switch {
		case errors.Is(err, filelock.ErrLocked):
			result.running++
			_ = taskRoot.Close()

			continue
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, fmt.Errorf("check the task %s: %w", name, err))
			_ = taskRoot.Close()

			continue
		}

		removedLock := false

		switch {
		// an aibox from before the lock was beside the folder may have
		// made the folder a moment ago and not yet its lock, or not yet its
		// lock file
		case strings.HasPrefix(name, newTaskPrefix):
			if startedBefore(entry, time.Now().Add(-newTaskGrace)) {
				if err := root.RemoveAll(name); err != nil {
					errs = append(errs, fmt.Errorf("remove %s: %w", name, err))
				}
			}

		// without an age every task that ended counts, also one whose folder
		// has a time after now, as a folder just made can have on Windows
		case !before.IsZero() && !startedBefore(entry, before):
		case all:
			if err := root.RemoveAll(name); err != nil {
				errs = append(errs, fmt.Errorf("remove the task %s: %w", name, err))
			} else {
				result.cleaned++
				removedLock = true
			}
		default:
			removed, err := removeInput(taskRoot)
			if err != nil {
				errs = append(errs, fmt.Errorf("remove the input of the task %s: %w", name, err))
			} else if removed {
				result.cleaned++
			}
		}

		if lock != nil {
			_ = lock.Close()
		}

		// once it is closed, as Windows removes no file that is open
		if removedLock {
			if err := root.Remove(name + lockSuffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove the lock of the task %s: %w", name, err))
			}
		}

		_ = taskRoot.Close()
	}

	return result, errors.Join(errs...)
}

// lockOf locks the task of the folder name in the folder of the tasks.
// It takes the lock in the folder, of a task of an aibox from before the
// lock was beside it, when there is none beside it, and returns
// fs.ErrNotExist when there is neither. The lock beside is opened as
// lockTask does, with every kind of sharing, so that another aibox can
// remove it on Windows while this one has it open.
func lockOf(tasks string, taskRoot *os.Root, name string) (*os.File, error) {
	lock, err := lockTask(filepath.Join(tasks, name), 0)
	if !errors.Is(err, fs.ErrNotExist) {
		return lock, err
	}

	if lock, err = taskRoot.OpenFile(lockFile, lockFlags, 0); err != nil {
		return nil, err
	}

	return lockOpened(lock)
}

// removeStrayLock removes the lock file of a task whose folder is not
// there, which a task that failed to make its folder leaves, once no one
// holds it and it is older than a task takes to make its folder. It goes
// by the age of the file, not of the ID, as a task may draw its ID a while
// before it makes the file. It leaves files whose names are of no task.
func removeStrayLock(tasks string, entry fs.DirEntry) error {
	name, ok := strings.CutSuffix(entry.Name(), lockSuffix)
	if !ok || !isTaskID(name) {
		return nil
	}

	info, err := entry.Info()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("check the lock %s: %w", entry.Name(), err)
	}

	if !info.ModTime().Before(time.Now().Add(-newTaskGrace)) {
		return nil
	}

	dir := filepath.Join(tasks, name)
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	lock, err := lockTask(dir, 0)

	switch {
	case errors.Is(err, filelock.ErrLocked), errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("check the lock %s: %w", entry.Name(), err)
	}

	_ = lock.Close()

	if err := os.Remove(lockPath(dir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove the lock %s: %w", entry.Name(), err)
	}

	return nil
}

// isTaskID tells whether the name is the ID of a task, by the time it
// starts with.
func isTaskID(name string) bool {
	_, err := taskStart(name)

	return err == nil
}

// taskStart is the time the task of the ID started, which the ID starts
// with.
func taskStart(id string) (time.Time, error) {
	return time.ParseInLocation(taskIDTime, id[:min(len(id), len(taskIDTime))], time.Local)
}

// removeInput removes the bundle of the input of the task in the folder,
// without following a link out of it. It tells whether there was one.
func removeInput(dir *os.Root) (bool, error) {
	switch err := dir.Remove(filepath.Join("share", task.InputBundle)); {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	}

	return true, nil
}

// startedBefore tells whether the task of the entry started before t: by
// the time in its name, or else by when its folder changed.
func startedBefore(entry fs.DirEntry, t time.Time) bool {
	if started, err := taskStart(entry.Name()); err == nil {
		return started.Before(t)
	}

	info, err := entry.Info()

	return err == nil && info.ModTime().Before(t)
}
