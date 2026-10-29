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
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/task"
)

// clonesFolder is the folder of the project folder that holds a folder for
// each session on a clone.
const clonesFolder = "clones"

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

// runClone runs the session on a clone and then says what came back. The
// folder of the session is made last, so that a run that fails before the
// VM starts leaves none that would count as one whose commits did not
// come back.
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

	spec.Clone = c.share
	spec.KeepClone = plan.keep
	spec.Then = &backend.Then{Stdout: resultsWriter, Stderr: vm, Timeout: cloneTimeout}

	before := sessions(r.project.Home)
	runErr := deps.backend.Run(ctx, spec)
	r.proxyLog.Summarize()

	_ = resultsWriter.Close()
	receiveErr := <-received

	_ = vm.Close()

	tellResume(deps.stderr, before, r.project.Home)

	var reportErr error

	switch {
	case receiveErr != nil:
		reportErr = fmt.Errorf("the commits of the clone did not come back, aibox run --mode clone --clone-keep goes on with it and gets them: %w", receiveErr)
		_, _ = fmt.Fprintf(host, "%v\n", reportErr)
	default:
		if reportErr = c.report(host, vm); reportErr != nil {
			_, _ = fmt.Fprintf(host, "%v\n", reportErr)
		}
	}

	_ = host.Close()

	if reportErr != nil {
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

	from := cmd.String("from")

	base, err := gitOutput(cwd, "", "rev-parse", "--verify", "--end-of-options", from+"^{commit}")
	if err != nil {
		return clonePlan{}, fmt.Errorf("%w: %s: %w", errCloneCommit, from, err)
	}

	// a bundle of a shallow clone would miss the history the clone has not
	if shallow, err := gitOutput(cwd, "", "rev-parse", "--is-shallow-repository"); err != nil || shallow != "false" {
		return clonePlan{}, errShallow
	}

	return clonePlan{base: base, remotes: hostRemotes(cwd, stderr)}, nil
}

// earlierClone is a session on a clone that ran before: its folder, the
// commit its clone started from, if aibox can tell, and whether its
// commits came back.
type earlierClone struct {
	dir  string
	base string
	done bool
}

// lastClone is the newest session on a clone of the project, or nil. The
// names of the folders start with the time, so the newest sorts last.
// Only aibox writes the folders, never the VM.
func lastClone(projectDir string) (*earlierClone, error) {
	clones := filepath.Join(projectDir, clonesFolder)

	entries, err := os.ReadDir(clones)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil //nolint:nilnil // no session ran
	}

	if err != nil {
		return nil, fmt.Errorf("look for the last session on a clone: %w", err)
	}

	newest := ""

	for _, entry := range entries {
		if entry.IsDir() && entry.Name() > newest {
			newest = entry.Name()
		}
	}

	if newest == "" {
		return nil, nil //nolint:nilnil // no session ran
	}

	c := &earlierClone{dir: filepath.Join(clones, newest)}

	// a folder aibox could not finish counts as one whose commits did not
	// come back
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

	started = true

	return c, nil
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
}

// report tells the person what came back from the clone and how to fetch
// it, and returns an error when the commits did not come back.
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

	// the VM said already when there are no changes
	switch {
	case err != nil:
		say("%v\n", err)
	case heads != nil:
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
