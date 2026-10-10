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
	"github.com/the127/aibox/internal/project"
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

	p, err := project.Open(filepath.Join(aibox, "projects"), cwd)
	if err != nil {
		return fmt.Errorf("open the project folder: %w", err)
	}

	result, err := cleanTasks(filepath.Join(p.Dir, "tasks"), all, time.Now().Add(-olderThan))

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
// before. A task that runs holds its lock, and its folder stays. A folder
// without a lock file counts as one of a task that ended. cleanTasks also
// removes what is left of folders of tasks that ended over a minute ago,
// before they got their name. It returns an error for every folder it
// could not check or clean.
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

		lock, err := taskRoot.OpenFile(lockFile, lockFlags, 0)
		if err == nil {
			lock, err = lockOpened(lock, false)
		}

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

		switch {
		// another aibox may have made the folder a moment ago and not yet
		// its lock, or not yet its lock file
		case strings.HasPrefix(name, newTaskPrefix):
			if startedBefore(entry, time.Now().Add(-newTaskGrace)) {
				if err := root.RemoveAll(name); err != nil {
					errs = append(errs, fmt.Errorf("remove %s: %w", name, err))
				}
			}
		case !startedBefore(entry, before):
		case all:
			if err := root.RemoveAll(name); err != nil {
				errs = append(errs, fmt.Errorf("remove the task %s: %w", name, err))
			} else {
				result.cleaned++
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

		_ = taskRoot.Close()
	}

	return result, errors.Join(errs...)
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
	if started, err := time.ParseInLocation(taskIDTime, entry.Name()[:min(len(entry.Name()), len(taskIDTime))], time.Local); err == nil {
		return started.Before(t)
	}

	info, err := entry.Info()

	return err == nil && info.ModTime().Before(t)
}
