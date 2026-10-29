package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/task"
)

// clonesFolder is the folder of the project folder that holds a folder for
// each session on a clone, and lastCloneFile the file that names the one
// whose clone the VM has.
const (
	clonesFolder  = "clones"
	lastCloneFile = "last-clone"
)

// taskIDPattern matches the IDs taskID makes.
var taskIDPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// cloneTimeout is how long the VM may take after the session on a clone to
// send what it did: its git steps have 10 minutes, and a bundle of up to
// 1 GiB has to come over.
const cloneTimeout = 15 * time.Minute

var (
	errCloneFlags    = errors.New("--clone-keep, --clone-discard and --from need --mode clone")
	errKeepDiscard   = errors.New("--clone-keep and --clone-discard don't go together")
	errKeepFrom      = errors.New("--clone-keep goes on with the clone of the last session, which starts where it started, so --from can't go with it")
	errNoCloneToKeep = errors.New("there is no clone of an earlier session to go on with")
	errCloneCommit   = errors.New("a clone must start in a git repository, from a commit")
	errUnknownStart  = errors.New("aibox can't tell which commit the clone of the last session started from, so --clone-discard is the way on")
)

// cloneLeftError is a session on a clone that ended before its commits
// came back. Its folder is dir.
type cloneLeftError struct {
	dir string
}

func (e cloneLeftError) Error() string {
	return fmt.Sprintf("the last session on a clone ended before its commits came back, see %s. "+
		"Run aibox run --mode clone --clone-keep to go on with that clone and get its commits at the end, or --clone-discard to throw it away", e.dir)
}

// runMode is the mode of aibox run: the --mode flag, else the one of the
// config, else mount. The flags of a clone need the mode clone.
func runMode(cmd *cli.Command, configured string) (string, error) {
	mode := configured
	if cmd.IsSet("mode") {
		mode = cmd.String("mode")
		if err := config.CheckMode(mode); err != nil {
			return "", fmt.Errorf("--mode %w", err)
		}
	}

	if mode == "" {
		mode = config.ModeMount
	}

	keep, discard, from := cmd.Bool("clone-keep"), cmd.Bool("clone-discard"), cmd.IsSet("from")

	switch {
	case mode != config.ModeClone && (keep || discard || from):
		return "", errCloneFlags
	case keep && discard:
		return "", errKeepDiscard
	case keep && from:
		return "", errKeepFrom
	}

	return mode, nil
}

// runClone runs the session on a clone and then says what came back. A VM
// that never connected left the clone of the last session as it was, so
// lastCloneFile names that session again.
func runClone(ctx context.Context, deps dependencies, r projectRun, spec backend.Spec, cwd string, plan clonePlan) error {
	c, err := newCloneRun(r.project.Dir, cwd, plan)
	if err != nil {
		return err
	}

	defer c.close()

	// what comes from the VM after the session carries the ID of the clone
	log := task.NewLog(deps.stderr, shortID(c.dir), time.Now)
	host, vm := log.Writer("aibox"), log.Writer("vm")

	results, resultsWriter := io.Pipe()
	received := make(chan error, 1)

	go func() {
		err := task.Receive(results, c.writers())

		// the VM waits for its writes to go through, so the rest of a
		// refused archive is read and dropped
		_, _ = io.Copy(io.Discard, results)

		received <- err
	}()

	var attached atomic.Bool

	spec.Clone = c.share
	spec.KeepClone = plan.keep
	spec.Then = &backend.Then{Stdout: resultsWriter, Stderr: vm, Timeout: cloneTimeout}
	spec.OnAttach = func() { attached.Store(true) }

	before := sessions(r.project.Home)
	runErr := deps.backend.Run(ctx, spec)
	r.proxyLog.Summarize()

	_ = resultsWriter.Close()
	receiveErr := <-received

	_ = vm.Close()

	defer func() { _ = host.Close() }()

	if !attached.Load() {
		if err := c.restoreLast(); err != nil {
			_, _ = fmt.Fprintf(host, "%v\n", err)
		}

		return runErr
	}

	tellResume(deps.stderr, before, r.project.Home)

	// why the VM stopped comes first, before what is missing because of
	// it. Its text may come from the VM.
	var exit *backend.ExitError
	if runErr != nil && !errors.As(runErr, &exit) {
		_, _ = fmt.Fprintf(host, "%s\n", task.CleanLine(runErr.Error()))
		runErr = printedError{runErr}
	}

	reportErr := c.report(host, vm)
	if receiveErr != nil {
		reportErr = fmt.Errorf("the commits of the clone did not come back, aibox run --mode clone --clone-keep goes on with it and gets them, --clone-discard throws it away: %w", receiveErr)
	}

	if reportErr != nil {
		_, _ = fmt.Fprintf(host, "%v\n", reportErr)

		return printedError{reportErr}
	}

	return runErr
}

// clonePlan is how a session on a clone starts: from the base, on a new
// clone with the remotes or on the clone of the last session.
type clonePlan struct {
	base    string
	keep    bool
	remotes []task.Remote
	// lastCameBack says that the session before has no commits to lose,
	// since they came back or there was none
	lastCameBack bool
}

// planClone decides how the session on a clone starts. A new clone would
// replace the clone of the last session, so it needs that session's
// commits to have come back, or --clone-discard.
func planClone(cmd *cli.Command, cwd, projectDir string, stderr io.Writer) (clonePlan, error) {
	last, err := lastClone(projectDir)
	if err != nil {
		return clonePlan{}, err
	}

	switch keep := cmd.Bool("clone-keep"); {
	case keep && last == nil:
		return clonePlan{}, errNoCloneToKeep
	case keep && last.base == "":
		return clonePlan{}, errUnknownStart
	case keep:
		return clonePlan{base: last.base, keep: true}, nil
	case last != nil && !last.done && !cmd.Bool("clone-discard"):
		return clonePlan{}, cloneLeftError{dir: last.dir}
	}

	lastCameBack := last == nil || last.done

	from := cmd.String("from")

	base, err := gitOutput(cwd, "", "rev-parse", "--verify", "--end-of-options", from+"^{commit}")
	if err != nil {
		return clonePlan{}, fmt.Errorf("%w: %s: %w", errCloneCommit, from, err)
	}

	// a bundle of a shallow clone would miss the history the clone has not
	if shallow, err := gitOutput(cwd, "", "rev-parse", "--is-shallow-repository"); err != nil || shallow != "false" {
		return clonePlan{}, errShallow
	}

	return clonePlan{base: base, remotes: hostRemotes(cwd, stderr), lastCameBack: lastCameBack}, nil
}

// earlierClone is a session on a clone that ran before: its folder, the
// commit its clone started from, if aibox can tell, and whether its
// commits came back.
type earlierClone struct {
	dir  string
	base string
	done bool
}

// lastClone is the session on a clone whose clone the VM has, or nil when
// it has none. last-clone in the project folder names it. Only aibox
// writes the folder of the project, never the VM.
func lastClone(projectDir string) (*earlierClone, error) {
	content, err := os.ReadFile(filepath.Join(projectDir, lastCloneFile)) //nolint:gosec // aibox's own folder
	if errors.Is(err, fs.ErrNotExist) || err == nil && strings.TrimSpace(string(content)) == "" {
		return nil, nil //nolint:nilnil // the VM has no clone yet
	}

	if err != nil {
		return nil, fmt.Errorf("read the last session on a clone: %w", err)
	}

	// a name aibox could not have written counts as a session whose
	// commits did not come back
	id := strings.TrimSpace(string(content))
	if !taskIDPattern.MatchString(id) {
		return &earlierClone{dir: filepath.Join(projectDir, lastCloneFile)}, nil
	}

	c := &earlierClone{dir: filepath.Join(projectDir, clonesFolder, id)}
	if c.base = cloneBase(c.dir); c.base != "" {
		c.done = cameBack(c.dir, c.base)
	}

	return c, nil
}

// cloneBase is the commit the clone of the session in dir started from,
// or empty when aibox can't tell.
func cloneBase(dir string) string {
	var settings task.Clone

	content, err := os.ReadFile(filepath.Join(dir, "share", task.CloneFile)) //nolint:gosec // aibox's own folder
	if err != nil || json.Unmarshal(content, &settings) != nil || settings.Check() != nil {
		return ""
	}

	return settings.Base
}

// cameBack says whether the commits of the session on a clone in dir came
// back: it sent its results without an error, and a bundle aibox takes or
// none for no changes.
func cameBack(dir, base string) bool {
	content, err := os.ReadFile(filepath.Join(dir, task.ResultFile)) //nolint:gosec // aibox's own folder
	if err != nil {
		return false
	}

	var result task.Result
	if json.Unmarshal(content, &result) != nil || result.Error != "" {
		return false
	}

	file, err := os.Open(filepath.Join(dir, task.ChangesFile)) //nolint:gosec // aibox's own folder
	if err != nil {
		return false
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return false
	}

	if info.Size() == 0 {
		return result.Head == base
	}

	bundle, err := task.ReadBundle(file)
	if err != nil {
		return false
	}

	_, err = bundle.CheckClone(base, task.CloneBranch, task.CloneEndBranch)

	return err == nil
}

// hostRemotes are the remotes of the repository in cwd that a clone can
// have. git in the VM reaches them through the git broker, which serves
// HTTPS addresses without a login and SSH addresses. The others are left
// out, and only their names are told, since a URL may hold a token.
func hostRemotes(cwd string, stderr io.Writer) []task.Remote {
	out, err := gitOutput(cwd, "", "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil || out == "" {
		return nil
	}

	var remotes []task.Remote

	seen := map[string]bool{}

	for line := range strings.SplitSeq(out, "\n") {
		key, url, _ := strings.Cut(line, " ")
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")

		// git fetches from the first URL of a remote
		if seen[name] {
			continue
		}

		seen[name] = true
		remote := task.Remote{Name: name, URL: url}

		if !task.CheckRemote(remote) {
			_, _ = fmt.Fprintf(stderr, "aibox: the clone gets no remote %s, since it gets only HTTPS addresses without a login and SSH addresses\n", task.CleanLine(name))

			continue
		}

		remotes = append(remotes, remote)
	}

	return remotes
}

// cloneRun is a session on a clone on its way: its folder below the
// project folder, with the share the VM gets and the files of the
// results.
type cloneRun struct {
	dir     string
	share   string
	plan    clonePlan
	results map[string]*os.File
	// last is lastCloneFile, open before the VM starts, since aibox can
	// open no file once it has locked itself down, and previous the session
	// it named before, if any
	last     *os.File
	previous string
}

// newCloneRun makes the folder of a new session on a clone.
func newCloneRun(projectDir, cwd string, plan clonePlan) (*cloneRun, error) {
	id, err := taskID()
	if err != nil {
		return nil, err
	}

	clones := filepath.Join(projectDir, clonesFolder)
	if err := os.MkdirAll(clones, 0o700); err != nil {
		return nil, fmt.Errorf("make the folder of the clones: %w", err)
	}

	c := &cloneRun{dir: filepath.Join(clones, id), plan: plan, results: map[string]*os.File{}}
	c.share = filepath.Join(c.dir, "share")

	// a session that cannot start leaves no folder, and removes only one
	// it made
	started, madeDir := false, false

	defer func() {
		if started {
			return
		}

		c.close()

		if madeDir {
			_ = os.RemoveAll(c.dir)
		}
	}()

	if err := os.Mkdir(c.dir, 0o700); err != nil {
		return nil, fmt.Errorf("make the folder of the clone: %w", err)
	}

	madeDir = true

	// the VM user stands for the person in the share, but on macOS it sees
	// the modes of the files as they are
	if err := os.MkdirAll(c.share, 0o755); err != nil { //nolint:gosec // the share holds nothing the VM may not read
		return nil, fmt.Errorf("make the folder of the clone: %w", err)
	}

	content, err := json.Marshal(task.Clone{Base: plan.base, Remotes: plan.remotes})
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(filepath.Join(c.share, task.CloneFile), content, 0o644); err != nil { //nolint:gosec // the VM reads it
		return nil, fmt.Errorf("write %s: %w", task.CloneFile, err)
	}

	if !plan.keep {
		bundle := filepath.Join(c.share, task.InputBundle)

		scratch, err := scratchRepository(c.dir)
		if err != nil {
			return nil, fmt.Errorf("make a repository for the bundle: %w", err)
		}

		if err := bundleCommit(cwd, plan.base, c.dir, bundle, scratch); err != nil {
			return nil, fmt.Errorf("bundle the commit the clone starts from: %w", err)
		}

		if err := os.Chmod(bundle, 0o644); err != nil { //nolint:gosec // the VM reads it
			return nil, err
		}
	}

	for _, name := range []string{task.ChangesFile, task.ResultFile} {
		file, err := os.OpenFile(filepath.Join(c.dir, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the names are aibox's own
		if err != nil {
			return nil, fmt.Errorf("make %s: %w", name, err)
		}

		c.results[name] = file
	}

	if err := c.nameLast(projectDir); err != nil {
		return nil, err
	}

	started = true

	return c, nil
}

// nameLast names the new session in lastCloneFile before the VM starts,
// since the VM may replace its clone as soon as it runs, and an aibox that
// is killed writes nothing afterwards. A new clone only replaces one whose
// commits came back, or that the person threw away, so naming it early
// loses nothing. The file stays open for restoreLast.
func (c *cloneRun) nameLast(projectDir string) error {
	file, err := os.OpenFile(filepath.Join(projectDir, lastCloneFile), os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // the name is aibox's own
	if err != nil {
		return fmt.Errorf("open %s: %w", lastCloneFile, err)
	}

	c.last = file

	previous, err := io.ReadAll(io.LimitReader(file, 64))
	if err != nil {
		return fmt.Errorf("read %s: %w", lastCloneFile, err)
	}

	if id := strings.TrimSpace(string(previous)); taskIDPattern.MatchString(id) {
		c.previous = id
	}

	// a longer file, edited by hand, keeps no tail. The ID comes first, so
	// that the file is never empty in between. On a failure the folder of
	// the new session goes, so the file names the last one again.
	if err := c.writeLast(filepath.Base(c.dir)); err != nil {
		_ = c.writeLast(c.previous)

		return err
	}

	if err := file.Truncate(int64(lastCloneBytes)); err != nil {
		_ = c.writeLast(c.previous)

		return fmt.Errorf("write %s: %w", lastCloneFile, err)
	}

	return nil
}

// restoreLast names the last session again in lastCloneFile, for a VM that
// never connected. A clone the person threw away stays unnamed, since the
// VM may have replaced it before it could connect.
func (c *cloneRun) restoreLast() error {
	if !c.plan.keep && !c.plan.lastCameBack {
		return nil
	}

	return c.writeLast(c.previous)
}

// lastCloneBytes is how long lastCloneFile is: an ID and a newline.
const lastCloneBytes = len("20060102-150405-000000\n")

// writeLast writes the ID, or nothing, padded to the length of an ID, so
// that the file never needs to shrink: aibox can truncate no file once it
// has locked itself down. It goes to the disk at once, since a session it
// names must not be forgotten in a crash of the host.
func (c *cloneRun) writeLast(id string) error {
	if _, err := c.last.WriteAt(fmt.Appendf(nil, "%-*s\n", lastCloneBytes-1, id), 0); err != nil {
		return fmt.Errorf("write %s: %w", lastCloneFile, err)
	}

	if err := c.last.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", lastCloneFile, err)
	}

	return nil
}

func (c *cloneRun) writers() map[string]io.Writer {
	writers := make(map[string]io.Writer, len(c.results))
	for name, file := range c.results {
		writers[name] = file
	}

	return writers
}

func (c *cloneRun) close() {
	for _, file := range c.results {
		_ = file.Close()
	}

	if c.last != nil {
		_ = c.last.Close()
	}
}

// report tells the person what came back from the clone and how to fetch
// it, and returns an error when the commits did not come back or their
// bundle is not one aibox takes.
func (c *cloneRun) report(host, vm io.Writer) error {
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(host, format, args...) }

	content, err := readAll(c.results[task.ResultFile])
	if err != nil {
		return err
	}

	var result task.Result
	if err := json.Unmarshal(content, &result); err != nil {
		return fmt.Errorf("%w: %s: %w", task.ErrBadResults, task.ResultFile, err)
	}

	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintf(vm, "warning: %s\n", task.CleanLine(warning))
	}

	heads, err := c.changes()

	// the VM said already when there are no changes, and the caller tells
	// the error
	if err == nil && heads != nil {
		id := filepath.Base(c.dir)
		refspecs := []string{refspec(task.CloneBranch, id)}

		say("the changes end at %s", short(heads[task.CloneBranch]))

		if end, ok := heads[task.CloneEndBranch]; ok {
			refspecs = append(refspecs, refspec(task.CloneEndBranch, id))
			say(", and the session ended apart from them at %s", short(end))
		}

		say(", fetch them with\n  git -c transfer.fsckObjects=true fetch %s %s\n", shellQuote(filepath.Join(c.dir, task.ChangesFile)), strings.Join(refspecs, " "))
	}

	say("the results are in %s\n", c.dir)

	if result.Error != "" {
		return fmt.Errorf("the commits of the clone did not come back: %s", task.CleanLine(result.Error))
	}

	return err
}

// refspec fetches the branch of the bundle into a branch of its own, named
// after the session.
func refspec(ref, id string) string {
	branch := strings.TrimPrefix(ref, "refs/heads/")

	return branch + ":" + branch + "-" + id
}

// changes checks the bundle of changes and returns the commits of the
// branches it carries, or nil when the VM sent none.
func (c *cloneRun) changes() (map[string]string, error) {
	file := c.results[task.ChangesFile]

	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return nil, err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	bundle, err := task.ReadBundle(file)
	if err != nil {
		return nil, err
	}

	return bundle.CheckClone(c.plan.base, task.CloneBranch, task.CloneEndBranch)
}
