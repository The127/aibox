package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/filelock"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/project"
)

const (
	// runLockFile is the file in the folder of a project that aibox run
	// holds the lock on while it runs, and that reset and remove take, so
	// that neither works in a home or on a disk a VM uses.
	runLockFile = "run.lock"
	// tasksLockFile is the file in the folder of a project that every task
	// holds a shared lock on while it runs, and that remove takes alone,
	// so that it removes no folder of a task that runs.
	tasksLockFile = "tasks.lock"
	// credentialsFile is where Claude Code on Linux keeps its login, in
	// .claude in the home folder.
	credentialsFile = ".credentials.json"
)

var (
	errProjectBusy    = errors.New("another aibox runs, resets or removes this project")
	errProjectRuns    = errors.New("the project runs, end aibox run first")
	errTaskRuns       = errors.New("a task of the project runs, wait for it to end")
	errProjectRemoved = errors.New("the project was removed while aibox started, start it again")
	errNoProject      = errors.New("aibox keeps nothing for this project")
	errNeedsYes       = errors.New("without a terminal to ask, pass --yes")
	errNotConfirmed   = errors.New("nothing changed")
	errStillThere     = errors.New("a new run or task of the project started meanwhile, so its folder stays")
	errNoProjectLock  = errors.New("the lock of the project is no plain file")
	errProjectExists  = errors.New("aibox keeps a folder for the project in the current folder already, remove it first with aibox projects remove")
)

func projectsCommand(deps dependencies) *cli.Command {
	yes := &cli.BoolFlag{Name: "yes", Usage: "don't ask before changing anything"}

	return &cli.Command{
		Name:  "projects",
		Usage: "what aibox keeps for each project in ~/.aibox/projects",
		Commands: []*cli.Command{{
			Name:  "list",
			Usage: "list the projects aibox keeps a folder for",
			Action: func(context.Context, *cli.Command) error {
				return runList(deps)
			},
		}, {
			Name:  "reset",
			Usage: "start the VM of the project in the current folder over: clear its home folder and its state disk, but keep the config, the tasks and the login",
			Flags: []cli.Flag{
				&cli.BoolFlag{Name: "logout", Usage: "clear the login too"},
				yes,
			},
			Action: func(ctx context.Context, cmd *cli.Command) error {
				return runReset(ctx, deps, cmd.Bool("logout"), cmd.Bool("yes"))
			},
		}, {
			Name:      "remove",
			Usage:     "remove everything aibox keeps for the project in the current folder, or at the path",
			ArgsUsage: "[path]",
			Flags:     []cli.Flag{yes},
			Action: func(ctx context.Context, cmd *cli.Command) error {
				return runRemove(ctx, deps, cmd.Args().First(), cmd.Bool("yes"))
			},
		}, {
			Name:  "move",
			Usage: "after you moved or renamed the project in the current folder, take along what aibox keeps for it at its old path",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "from", Usage: "the old path of the project", Required: true},
				yes,
			},
			Action: func(ctx context.Context, cmd *cli.Command) error {
				return runMove(ctx, deps, cmd.String("from"), cmd.Bool("yes"))
			},
		}},
	}
}

func runList(deps dependencies) error {
	_, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	base := filepath.Join(aibox, "projects")

	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read %s: %w", base, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		p := project.In(filepath.Join(base, entry.Name()))

		written, err := os.ReadFile(p.Path)
		if err != nil {
			_, _ = fmt.Fprintf(deps.stdout, "%s (folder %s, its path is known after its next run)\n", entry.Name(), p.Dir)

			continue
		}

		path := strings.TrimSpace(string(written))

		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) { //nolint:gosec // aibox wrote the path, and Stat only looks
			_, _ = fmt.Fprintf(deps.stdout, "%s (the folder is gone)\n", path)

			continue
		}

		_, _ = fmt.Fprintln(deps.stdout, path)
	}

	return nil
}

func runReset(ctx context.Context, deps dependencies, logout, yes bool) error {
	cwd, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	p, err := existingProject(aibox, cwd)
	if err != nil {
		return err
	}

	cleared := "its home folder, apart from the login, and its state disk"
	if logout {
		cleared = "its home folder with the login, and its state disk"
	}

	if err := confirm(ctx, deps, fmt.Sprintf("Start the VM of %s over? This clears %s.", cwd, cleared), yes); err != nil {
		return err
	}

	runLock, err := lockRun(p)
	if err != nil {
		return err
	}

	defer func() { _ = runLock.Close() }()

	if err := removeState(p); err != nil {
		return err
	}

	if err := clearHome(p, logout); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(deps.stderr, "aibox: cleared %s\n", cleared)

	return nil
}

func runRemove(ctx context.Context, deps dependencies, path string, yes bool) error {
	cwd, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	if path == "" {
		path = cwd
	} else {
		path = absolute(cwd, path)
	}

	p, err := existingProject(aibox, path)
	if err != nil {
		return err
	}

	if err := confirm(ctx, deps, fmt.Sprintf("Remove everything aibox keeps for %s? That is its config, the VM's home folder with the login, the state disk, the logs and the tasks.", path), yes); err != nil {
		return err
	}

	release, err := lockAlone(p)
	if err != nil {
		return err
	}

	err = removeProject(p)

	// Windows removes the lock files once they are closed, and only then
	// the folder they were in
	release()

	if err != nil {
		return err
	}

	if err := os.Remove(p.Dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if entries, _ := os.ReadDir(p.Dir); len(entries) > 0 {
			return errStillThere
		}

		return fmt.Errorf("remove the folder of the project: %w", err)
	}

	_, _ = fmt.Fprintf(deps.stderr, "aibox: removed everything aibox kept for %s\n", path)

	return nil
}

func runMove(ctx context.Context, deps dependencies, from string, yes bool) error {
	cwd, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	from = absolute(cwd, from)

	old, err := existingProject(aibox, from)
	if err != nil {
		return err
	}

	base := filepath.Join(aibox, "projects")

	p, err := project.At(base, cwd)
	if err != nil {
		return err
	}

	// on macOS and Windows a rename that only changes case keeps the
	// folder, and only its path file is out of date
	same, err := sameFolder(old.Dir, p.Dir)
	if err != nil {
		return err
	}

	if same {
		if _, err := project.Open(base, cwd); err != nil {
			return err
		}

		_, _ = fmt.Fprintf(deps.stderr, "aibox: aibox finds what it keeps for %s already, there is nothing to move\n", cwd)

		return nil
	}

	if err := emptyProject(p.Dir); err != nil {
		return err
	}

	if err := confirm(ctx, deps, fmt.Sprintf("Move everything aibox keeps for %s to %s? That is its config, the VM's home folder with the login, the state disk, the logs and the tasks.", from, cwd), yes); err != nil {
		return err
	}

	releaseOld, err := lockAlone(old)
	if err != nil {
		return err
	}

	defer func() { releaseOld() }()

	releaseNew, err := lockAlone(p)
	if err != nil {
		return err
	}

	defer releaseNew()

	// checked again under the lock, as a run may have started there while
	// aibox asked
	if err := emptyProject(p.Dir); err != nil {
		return err
	}

	if err := moveEntries(old.Dir, p.Dir); err != nil {
		return err
	}

	if _, err := project.Open(base, cwd); err != nil {
		return fmt.Errorf("everything aibox kept for %s is in %s now, but %w", from, p.Dir, err)
	}

	if err := removeProject(old); err != nil {
		return fmt.Errorf("everything aibox kept for %s is in %s now, but %w", from, p.Dir, err)
	}

	// Windows removes the lock files once they are closed, and only then
	// the folder they were in
	releaseOld()
	releaseOld = func() {}

	// a run that started at the old path meanwhile keeps the folder, as it
	// is a new project there
	if err := os.Remove(old.Dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if entries, _ := os.ReadDir(old.Dir); len(entries) == 0 {
			return fmt.Errorf("remove the old folder of the project: %w", err)
		}
	}

	_, _ = fmt.Fprintf(deps.stderr, "aibox: moved everything aibox kept for %s to %s\n", from, cwd)

	return nil
}

// lockAlone takes the lock of aibox run and the tasks lock of the project,
// both alone, so that neither a run nor a task uses its folder.
func lockAlone(p project.Project) (release func(), err error) {
	runLock, err := lockRun(p)
	if err != nil {
		return nil, err
	}

	tasksLock, err := lockProject(p.Dir, tasksLockFile, false)
	if errors.Is(err, filelock.ErrLocked) {
		err = errTaskRuns
	}

	if err != nil {
		_ = runLock.Close()

		return nil, err
	}

	return func() {
		_ = runLock.Close()
		_ = tasksLock.Close()
	}, nil
}

// sameFolder says whether both folders exist and are one: the same name,
// or two names that differ only in case on macOS and Windows.
func sameFolder(a, b string) (bool, error) {
	infoA, err := os.Stat(a)
	if err != nil {
		return false, err
	}

	infoB, err := os.Stat(b)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return os.SameFile(infoA, infoB), nil
}

// emptyProject fails with errProjectExists when the folder of a project
// holds more than lock files, which a run that never started leaves.
func emptyProject(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.Name() != runLockFile && entry.Name() != tasksLockFile {
			return errProjectExists
		}
	}

	return nil
}

// moveEntries moves what the folder from holds into the folder to, apart
// from the lock files, which stay locked in from. Renaming the folder
// itself would fail on Windows while the lock files in it are open. If an
// entry fails to move, the ones moved already go back.
func moveEntries(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return fmt.Errorf("read the folder of the project: %w", err)
	}

	var moved []string

	for _, entry := range entries {
		name := entry.Name()
		if name == runLockFile || name == tasksLockFile {
			continue
		}

		if err := os.Rename(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			err = fmt.Errorf("move %s of the project: %w", name, err)

			for _, back := range moved {
				if backErr := os.Rename(filepath.Join(to, back), filepath.Join(from, back)); backErr != nil {
					err = errors.Join(err, fmt.Errorf("move %s back, it stays in %s: %w", back, to, backErr))
				}
			}

			return err
		}

		moved = append(moved, name)
	}

	return nil
}

// absolute returns the path, made absolute against cwd when it is
// relative.
func absolute(cwd, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}

	return spelled(path)
}

// existingProject returns the folders of the project at path, or
// errNoProject when aibox keeps no folder for it.
func existingProject(aibox, path string) (project.Project, error) {
	p, err := project.At(filepath.Join(aibox, "projects"), path)
	if err != nil {
		return project.Project{}, err
	}

	if _, err := os.Lstat(p.Dir); errors.Is(err, fs.ErrNotExist) {
		return project.Project{}, fmt.Errorf("%w: %s", errNoProject, path)
	}

	return p, nil
}

// confirm asks the question on the terminal, unless yes says it needn't,
// and fails with errNotConfirmed unless the answer is yes.
func confirm(ctx context.Context, deps dependencies, question string, yes bool) error {
	if yes {
		return nil
	}

	if !deps.stdinIsTerminal() {
		return errNeedsYes
	}

	_, _ = fmt.Fprintf(deps.stderr, "%s [y/N] ", question)

	answer, err := readAnswer(ctx, bufio.NewReader(deps.stdin))
	if err != nil {
		return err
	}

	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}

	return errNotConfirmed
}

// readAnswer reads a line from answers, until Ctrl-C ends ctx. The end of
// the input is an empty answer.
func readAnswer(ctx context.Context, answers *bufio.Reader) (string, error) {
	type read struct {
		line string
		err  error
	}

	done := make(chan read, 1)

	go func() {
		line, err := answers.ReadString('\n')
		done <- read{line, err}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-done:
		if r.err != nil && !errors.Is(r.err, io.EOF) {
			return "", fmt.Errorf("read the answer: %w", r.err)
		}

		return r.line, nil
	}
}

// lockRun takes the lock of the project that aibox run holds, and, for a
// run of an aibox from before that lock, checks the lock of the state disk.
func lockRun(p project.Project) (*os.File, error) {
	lock, err := lockProject(p.Dir, runLockFile, false)
	if errors.Is(err, filelock.ErrLocked) {
		return nil, errProjectRuns
	}

	if err != nil {
		return nil, err
	}

	state, err := machine.LockState(p.State)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return lock, nil
	case errors.Is(err, machine.ErrStateBusy):
		err = errProjectRuns
	case err == nil:
		// closed again before the disk is removed, as Windows removes no
		// file that is open
		err = state.Close()
	}

	if err != nil {
		_ = lock.Close()

		return nil, err
	}

	return lock, nil
}

// lockRunOf takes the lock of the project in the folder dir that aibox
// run holds while it runs.
func lockRunOf(dir string) (*os.File, error) {
	lock, err := lockProject(dir, runLockFile, false)
	if errors.Is(err, filelock.ErrLocked) {
		return nil, errProjectBusy
	}

	return lock, err
}

// lockTasksOf takes the shared lock of the project in the folder dir that
// every task holds while it runs. It is shared, as tasks run side by side,
// and keeps a remove from taking the folder of one.
func lockTasksOf(dir string) (*os.File, error) {
	lock, err := lockProject(dir, tasksLockFile, true)
	if errors.Is(err, filelock.ErrLocked) {
		return nil, fmt.Errorf("%w: aibox removes it", errProjectBusy)
	}

	return lock, err
}

// lockProject opens the lock file of the name in the folder of the
// project, and makes both when they are missing. It locks the file alone,
// or shared with others, without waiting. It fails with errProjectRemoved
// when the file it locked is no longer at its name, since remove took it
// away meanwhile.
func lockProject(dir, name string, shared bool) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}

	path := filepath.Join(dir, name)

	file, err := openNoFollow(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errProjectRemoved
	}

	if err != nil {
		return nil, err
	}

	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = file.Close()

		return nil, errors.Join(errNoProjectLock, err)
	}

	lock := filelock.Lock
	if shared {
		lock = filelock.LockShared
	}

	if err := lock(file, false); err != nil {
		_ = file.Close()

		return nil, err
	}

	if err := stillNamed(file, path); err != nil {
		_ = file.Close()

		if errors.Is(err, errLockTaken) || errors.Is(err, fs.ErrNotExist) {
			return nil, errProjectRemoved
		}

		return nil, err
	}

	return file, nil
}

// removeState removes the state disk of the project. The next run makes a
// new one.
func removeState(p project.Project) error {
	if err := os.Remove(p.State); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove the state disk: %w", err)
	}

	return nil
}

// clearHome removes what the home folder of the project holds, apart from
// the login of Claude Code unless logout says to remove that too. It
// keeps the login only where it is a plain file in a plain folder, since
// the VM writes the home and may have put a link there. It works in a
// root, so that no link leads it out of the home.
func clearHome(p project.Project, logout bool) error {
	home, err := os.OpenRoot(p.Home)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("open the home folder: %w", err)
	}

	defer func() { _ = home.Close() }()

	keep := func(dir string, entry fs.DirEntry) bool {
		if logout {
			return false
		}

		switch dir {
		case ".":
			return entry.Name() == ".claude" && entry.IsDir()
		case ".claude":
			return entry.Name() == credentialsFile && entry.Type().IsRegular()
		}

		return false
	}

	if err := clearIn(home, ".", keep); err != nil {
		return fmt.Errorf("clear the home folder: %w", err)
	}

	return nil
}

// clearIn removes every entry of the folder dir in the root, apart from
// those keep keeps. A folder it keeps is cleared the same way.
func clearIn(root *os.Root, dir string, keep func(dir string, entry fs.DirEntry) bool) error {
	// the VM may have taken away the rights to read or change the folder
	if err := root.Chmod(dir, 0o700); err != nil {
		return err
	}

	entries, err := fs.ReadDir(root.FS(), dir)
	if err != nil {
		return err
	}

	var errs []error

	for _, entry := range entries {
		name := filepath.Join(dir, entry.Name())

		switch {
		case !keep(dir, entry):
			if err := removeAll(root, name); err != nil {
				errs = append(errs, err)
			}
		case entry.IsDir():
			if err := clearIn(root, name, keep); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

// removeAll removes the name in the root and everything below it. When the
// VM took away the rights to change a folder below, which the files it
// writes keep on the host, it gives them back and tries again.
func removeAll(root *os.Root, name string) error {
	err := root.RemoveAll(name)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}

	if err := writable(root, name); err != nil {
		return err
	}

	return root.RemoveAll(name)
}

// writable gives the folder name in the root, and every folder below it,
// the rights to read and change it. It follows no link.
func writable(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() {
		return err
	}

	if err := root.Chmod(name, 0o700); err != nil {
		return err
	}

	entries, err := fs.ReadDir(root.FS(), name)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			if err := writable(root, filepath.Join(name, entry.Name())); err != nil {
				return err
			}
		}
	}

	return nil
}

// removeProject removes what the folder of the project holds, the lock
// files last, while their locks are held. The path file goes with them, so
// that a remove that fails halfway leaves a folder list still names.
func removeProject(p project.Project) error {
	dir, err := os.OpenRoot(p.Dir)
	if err != nil {
		return fmt.Errorf("open the folder of the project: %w", err)
	}

	defer func() { _ = dir.Close() }()

	last := []string{filepath.Base(p.Path), runLockFile, tasksLockFile}

	if err := clearIn(dir, ".", func(_ string, entry fs.DirEntry) bool { return slices.Contains(last, entry.Name()) }); err != nil {
		return fmt.Errorf("remove the folder of the project: %w", err)
	}

	for _, name := range last {
		if err := dir.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the folder of the project: %w", err)
		}
	}

	return nil
}
