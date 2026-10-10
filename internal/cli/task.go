package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/filelock"
	"github.com/the127/aibox/internal/proxy"
	"github.com/the127/aibox/internal/task"
)

// maxTimeout is the longest --timeout, far beyond any task, so that the
// time to stop the VM cannot overflow.
const maxTimeout = 30 * 24 * time.Hour

var (
	errNoPrompt     = errors.New("task needs a prompt, as arguments, with --file, or on stdin")
	errLongPrompt   = errors.New("the prompt is longer than 1 MiB, its arguments and its file together")
	errNoText       = errors.New("the prompt is not UTF-8 text")
	errNoLockFile   = errors.New("the lock of the task is no plain file")
	errNoPromptFile = errors.New("the file of the prompt must be a plain file, use --file - for a pipe")
	errClaudeFailed = errors.New("the task did not finish, see the transcript")
	errNoCommit     = errors.New("task must start in a git repository, from a commit")
	errShallow      = errors.New("a shallow clone lacks history the task needs, git fetch --unshallow fetches it")
	errNoTimeout    = errors.New("--timeout must be at least a second and at most 30 days")
)

// taskSlack is how much longer than its timeout a task may take before
// aibox stops the VM: the boot and the git steps before and after Claude
// Code, which have 10 minutes each in the VM.
const taskSlack = 25 * time.Minute

// proxyLogFile is the file in the folder of a task that the proxy writes
// the targets of the task to.
const proxyLogFile = "proxy.log"

// maxListedTargets is how many targets of each kind the report of a task
// names. The log of the proxy has them all.
const maxListedTargets = 5

// maxPromptBytes is how long the prompt of a task may be, its arguments and
// its file together.
const maxPromptBytes = 1 << 20

// credentials are the variables Claude Code logs in with. A task starts
// with an empty home, so one of them must reach the VM.
var credentials = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

func taskCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:      "task",
		Usage:     "run Claude Code unattended on the last commit, in a VM of its own, and keep its commits as a git bundle",
		ArgsUsage: "[PROMPT]",
		Flags: append(vmFlags(),
			&cli.StringFlag{Name: "file", Aliases: []string{"f"}, TakesFile: true, Usage: "read the prompt from this file, after the arguments, - for stdin. Without arguments, aibox reads stdin unless it is a terminal"},
			&cli.StringFlag{Name: "from", Usage: "the branch, tag or commit the task starts from", Value: "HEAD"},
			&cli.StringFlag{Name: "model", Usage: "the model Claude Code uses", DefaultText: "the default of Claude Code"},
			&cli.IntFlag{Name: "max-turns", Usage: "the most turns Claude Code takes", DefaultText: "no limit"},
			&cli.FloatFlag{Name: "max-budget-usd", Usage: "the most Claude Code may spend by its own estimate, in US dollars at API prices", DefaultText: "no limit"},
			&cli.DurationFlag{Name: "timeout", Usage: "how long Claude Code may work before it is stopped", Value: time.Hour},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runTask(ctx, deps, cmd)
		},
	}
}

// taskRun is a task on the host: its folder, the folder shared into the VM
// and the files the results go into, opened before the VM starts, since
// aibox can open no file once the VM runs.
type taskRun struct {
	dir     string
	share   string
	base    string
	results map[string]*os.File
	// proxyLog is where the proxy writes the targets of this task alone
	proxyLog *os.File
	// lock is locked for as long as the task runs, see cleanTasks
	lock *os.File
}

func runTask(ctx context.Context, deps dependencies, cmd *cli.Command) (err error) {
	if deps.uid() == 0 {
		return errRoot
	}

	if promptFile(deps, cmd) == "" && strings.TrimSpace(strings.Join(cmd.Args().Slice(), " ")) == "" {
		return errNoPrompt
	}

	if cmd.Duration("timeout") < time.Second || cmd.Duration("timeout") > maxTimeout {
		return errNoTimeout
	}

	cwd, aibox, err := folders(deps)
	if err != nil {
		return err
	}

	home, err := deps.homeDir()
	if err != nil {
		return fmt.Errorf("find the home directory: %w", err)
	}

	// the history of the repository goes into the VM
	if err := refuseUnsafeFolder(cwd, home, aibox); err != nil {
		return err
	}

	from := cmd.String("from")

	base, err := gitOutput(cwd, "rev-parse", "--verify", "--end-of-options", from+"^{commit}")
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errNoCommit, from, err)
	}

	// a bundle of a shallow clone would miss the history the clone has not
	if shallow, err := gitOutput(cwd, "rev-parse", "--is-shallow-repository"); err != nil || shallow != "false" {
		return errShallow
	}

	prompt, err := readPrompt(ctx, deps, cmd, cwd)
	if err != nil {
		return err
	}

	settings := task.Settings{
		Model:          cmd.String("model"),
		MaxTurns:       cmd.Int("max-turns"),
		MaxBudgetUSD:   cmd.Float("max-budget-usd"),
		TimeoutSeconds: int(cmd.Duration("timeout") / time.Second),
	}

	identity := deps.gitIdentity(cwd)
	settings.GitName, settings.GitEmail = identity.Name, identity.Email

	if err := settings.Check(); err != nil {
		return err
	}

	r, err := openProjectRun(ctx, deps, cmd, cwd, aibox)
	if err != nil {
		return err
	}

	defer r.close()

	warnings(deps.stderr, r.env)

	t, err := newTaskRun(r.project.Dir, cwd, base, prompt, settings)
	if err != nil {
		return err
	}

	defer t.close()

	// from here on, every line of the task carries its ID, the error too
	log := task.NewLog(deps.stderr, shortID(t.dir), time.Now)
	host, vm := log.Writer("aibox"), log.Writer("vm")

	defer func() { err = finishLog(host, vm, err) }()

	// the full name, since a name may stand for a tag or a ref that a VM
	// wrote into .git as well as for the branch the person means
	start := "the last commit " + short(base)
	if name, err := gitOutput(cwd, "rev-parse", "--verify", "--symbolic-full-name", "--end-of-options", from); err == nil && name != "" && name != "HEAD" && from != "HEAD" {
		start = fmt.Sprintf("%s (%s)", task.CleanLine(name), short(base))
	} else if head, _ := gitOutput(cwd, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); head != base {
		start = short(base)
	}

	_, _ = fmt.Fprintf(host, "task %s starts from %s, booting the VM\n", filepath.Base(t.dir), start)

	spec := r.spec(cmd)
	spec.Stderr = host
	spec.State = filepath.Join(t.dir, "state.ext4")
	spec.RemoveState = true
	spec.Task = t.share
	spec.ConsoleLog = filepath.Join(t.dir, "console.log")

	// the targets of this task, apart from those of other runs
	network := proxy.NewLog(t.proxyLog)
	spec.Proxy.OnConnected, spec.Proxy.OnRefused = network.Connected, network.Refused
	spec.Proxy.Local = r.local(t.proxyLog)

	progress := task.NewLineCleaner(vm)
	spec.Progress = progress

	results, resultsWriter := io.Pipe()
	spec.Stdout = resultsWriter

	limit := cmd.Duration("timeout") + taskSlack

	ctx, cancelTimeout := context.WithTimeout(ctx, limit)
	defer cancelTimeout()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	received := make(chan error, 1)

	var ran atomic.Bool

	go func() {
		err := task.Receive(results, t.writers())
		_ = results.CloseWithError(errors.Join(err, io.ErrClosedPipe))

		// a VM whose results are refused would wait for its writes to go
		// through, so it is stopped
		if err != nil && !ran.Load() {
			cancel(err)
		}

		received <- err
	}()

	runErr := deps.backend.Run(ctx, spec)

	// this comes before the pipe closes, since the close makes Receive end
	// with an error that is no refusal
	ran.Store(true)

	_ = resultsWriter.Close()
	_ = progress.Close()

	receiveErr := <-received

	reportNetwork(host, network)

	// aibox can remove it until it is confined, and the backend removes it
	// once the VM has it, so it is gone either way
	_ = os.Remove(spec.State)

	if err := outcome(ctx, runErr, receiveErr, limit); err != nil {
		_, _ = fmt.Fprintln(deps.stdout, t.dir)
		_, _ = fmt.Fprintf(host, "what the task left is in %s\n", t.dir)

		return err
	}

	return t.report(deps.stdout, host, vm)
}

// finishLog writes what is left in the writers of the log, and the error
// with the ID of the task, so that aibox does not print it once more.
func finishLog(host, vm *task.LogWriter, err error) error {
	_ = vm.Close()

	// aibox ends without a word when it is interrupted
	if err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(host, "%v\n", err)
		err = printedError{err}
	}

	_ = host.Close()

	return err
}

// reportNetwork says which targets the proxy connected to and which it
// refused. The proxy on the host saw them, so this is no word of the VM,
// but it shows only what the VM asked for, not what went through.
func reportNetwork(host io.Writer, log *proxy.Log) {
	connected, refused := log.Targets()

	if len(connected) > 0 {
		_, _ = fmt.Fprintf(host, "the VM connected to %s\n", listTargets(connected))
	}

	if len(refused) > 0 {
		_, _ = fmt.Fprintf(host, "the proxy refused %s\n", listTargets(refused))
	}
}

// listTargets names the first targets, cleaned, since the VM chose them.
func listTargets(targets []string) string {
	names := make([]string, 0, maxListedTargets)
	for _, target := range targets[:min(len(targets), maxListedTargets)] {
		names = append(names, task.CleanLine(target))
	}

	list := strings.Join(names, ", ")

	if more := len(targets) - len(names); more > 0 {
		list += fmt.Sprintf(" and %d more", more)
	}

	return list
}

// shortID is the random end of the ID of a task, which tells it apart from
// the tasks that ran with it.
func shortID(dir string) string {
	id := filepath.Base(dir)

	return id[strings.LastIndex(id, "-")+1:]
}

// outcome is the one error that says why the run of the task did not
// deliver its results, if it did not. A refusal of the results is the
// reason the VM stopped, a VM that failed is the reason the results were
// cut short. A failed task ends the session with an exit code, and
// result.json says why. The texts of errors may come from the VM, through
// SSH, so they are cleaned.
func outcome(ctx context.Context, runErr, receiveErr error, limit time.Duration) error {
	var exit *backend.ExitError

	switch {
	case receiveErr != nil && errors.Is(context.Cause(ctx), receiveErr):
		return receiveErr
	case errors.Is(runErr, context.DeadlineExceeded):
		return fmt.Errorf("the task took longer than %v and was stopped", limit)
	case errors.As(runErr, &exit), runErr == nil:
		return receiveErr
	}

	return errors.New(task.CleanLine(runErr.Error()))
}

// promptFile is the file of --file, or - for stdin when there are no
// arguments and stdin is no terminal, or nothing.
func promptFile(deps dependencies, cmd *cli.Command) string {
	if file := cmd.String("file"); file != "" || strings.TrimSpace(strings.Join(cmd.Args().Slice(), "")) != "" || deps.stdinIsTerminal() {
		return file
	}

	return "-"
}

// readPrompt returns the prompt of the task: its arguments, then the text
// of its file.
func readPrompt(ctx context.Context, deps dependencies, cmd *cli.Command, cwd string) (string, error) {
	parts := []string{strings.Join(cmd.Args().Slice(), " ")}

	if file := promptFile(deps, cmd); file != "" {
		if file == "-" && cmd.String("file") == "" {
			_, _ = fmt.Fprintln(deps.stderr, "aibox: reading the prompt from stdin")
		}

		text, err := readPromptFile(ctx, deps.stdin, cwd, file)
		if err != nil {
			return "", err
		}

		parts = append(parts, text)
	}

	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}

	prompt := strings.TrimSpace(strings.Join(parts, "\n\n"))

	switch {
	case prompt == "":
		return "", errNoPrompt
	case len(prompt) > maxPromptBytes:
		return "", errLongPrompt
	case !utf8.ValidString(prompt):
		return "", errNoText
	}

	return prompt, nil
}

// openPromptFile opens the file of --file. A file in a repository someone
// else wrote may be a link to a secret of the person, or a FIFO that never
// ends. So a relative path must stay inside cwd, links included, an
// absolute path must not end in a link, and opening a FIFO must not wait.
// readPromptFile then refuses anything but a plain file.
func openPromptFile(cwd, name string) (*os.File, error) {
	const flags = os.O_RDONLY | noFollowFlags

	if filepath.IsAbs(name) {
		return openNoFollow(name, os.O_RDONLY, 0)
	}

	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	if err := refuseLink(root, name); err != nil {
		return nil, err
	}

	return root.OpenFile(name, flags, 0)
}

// readPromptFile reads the file of --file, relative to cwd, or stdin for
// -. It refuses a link and anything but a plain file, and a file longer
// than a prompt may be. Ctrl-C stops it while it waits for stdin.
func readPromptFile(ctx context.Context, stdin io.Reader, cwd, name string) (string, error) {
	r := stdin

	if name != "-" {
		file, err := openPromptFile(cwd, name)
		if err != nil {
			return "", fmt.Errorf("read the prompt: %w", err)
		}

		defer func() { _ = file.Close() }()

		info, err := file.Stat()
		if err != nil {
			return "", fmt.Errorf("read the prompt: %w", err)
		}

		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: %s", errNoPromptFile, name)
		}

		r = file
	}

	type read struct {
		text []byte
		err  error
	}

	done := make(chan read, 1)

	go func() {
		text, err := io.ReadAll(io.LimitReader(r, maxPromptBytes+1))
		done <- read{text: text, err: err}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case got := <-done:
		switch {
		case got.err != nil:
			return "", fmt.Errorf("read the prompt: %w", got.err)
		case len(got.text) > maxPromptBytes:
			return "", errLongPrompt
		}

		return string(got.text), nil
	}
}

// warnings tells the person when the config passes no credential, since a
// task starts with no login. It does not look for changes not committed:
// git status may run a filter of .git/config, which the VM may have
// written.
func warnings(stderr io.Writer, env []string) {
	if !slices.ContainsFunc(env, func(variable string) bool {
		name, _, _ := strings.Cut(variable, "=")

		return slices.Contains(credentials, name)
	}) {
		_, _ = fmt.Fprintf(stderr, "aibox: a task starts with an empty home, so Claude Code needs %s from env in the config to log in\n", strings.Join(credentials, " or "))
	}
}

// newTaskRun makes the folder of a new task below the project folder, with
// the share the VM gets and the files of the results.
func newTaskRun(projectDir, cwd, base, prompt string, settings task.Settings) (*taskRun, error) {
	id, err := taskID()
	if err != nil {
		return nil, err
	}

	tasks := filepath.Join(projectDir, "tasks")
	if err := os.MkdirAll(tasks, 0o700); err != nil {
		return nil, fmt.Errorf("make the folder of the tasks: %w", err)
	}

	// the folder gets its name only once it holds its lock, so that no
	// other task takes it for the folder of a task that ended
	newDir, err := os.MkdirTemp(tasks, newTaskPrefix)
	if err != nil {
		return nil, fmt.Errorf("make the folder of the task: %w", err)
	}

	t := &taskRun{dir: newDir, base: base, results: map[string]*os.File{}}

	// a task that cannot start leaves neither its lock nor its folder
	started := false

	defer func() {
		if !started {
			t.close()

			_ = os.RemoveAll(t.dir)
		}
	}()

	// another aibox may hold the lock for a moment, as it looks for tasks
	// that ended
	if t.lock, err = lockTask(newDir, os.O_CREATE, true); err != nil {
		return nil, fmt.Errorf("lock the task: %w", err)
	}

	if err := os.Rename(newDir, filepath.Join(tasks, id)); err != nil {
		return nil, fmt.Errorf("name the folder of the task: %w", err)
	}

	t.dir = filepath.Join(tasks, id)
	t.share = filepath.Join(t.dir, "share")

	// the VM user stands for the person in the share, but on macOS it sees
	// the modes of the files as they are
	if err := os.MkdirAll(t.share, 0o755); err != nil { //nolint:gosec // the share holds nothing the VM may not read
		return nil, fmt.Errorf("make the folder of the task: %w", err)
	}

	content, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}

	for name, data := range map[string][]byte{task.SettingsFile: content, task.PromptFile: []byte(prompt + "\n")} {
		if err := os.WriteFile(filepath.Join(t.share, name), data, 0o644); err != nil { //nolint:gosec // the VM reads it
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
	}

	bundle := filepath.Join(t.share, task.InputBundle)
	if err := bundleCommit(cwd, base, bundle, filepath.Join(t.dir, "input.git")); err != nil {
		return nil, fmt.Errorf("bundle the commit the task starts from: %w", err)
	}

	if err := os.Chmod(bundle, 0o644); err != nil { //nolint:gosec // the VM reads it
		return nil, err
	}

	for _, name := range []string{task.TranscriptFile, task.LogFile, task.ChangesFile, task.ResultFile} {
		file, err := os.OpenFile(filepath.Join(t.dir, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the names are aibox's own
		if err != nil {
			return nil, fmt.Errorf("make %s: %w", name, err)
		}

		t.results[name] = file
	}

	// aibox writes it, never the VM
	if t.proxyLog, err = os.OpenFile(filepath.Join(t.dir, proxyLogFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
		return nil, fmt.Errorf("make %s: %w", proxyLogFile, err)
	}

	started = true

	return t, nil
}

// bundleCommit writes a bundle of the commit and its history, whose HEAD is
// the commit. git bundles refs only, so the bundle comes from an empty
// repository in tmp that borrows the objects of the project. The project
// gets no new ref, and a commit made there meanwhile changes nothing.
func bundleCommit(cwd, commit, bundle, tmp string) error {
	gitDir, err := gitOutput(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(tmp) }()

	// the repository runs no hooks, neither of a template nor of the config
	if _, err := gitOutput(cwd, "init", "--quiet", "--bare", "--template=", tmp); err != nil {
		return err
	}

	alternates := filepath.Join(tmp, "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(gitDir, "objects")+"\n"), 0o600); err != nil {
		return err
	}

	for _, args := range [][]string{
		{"update-ref", "refs/heads/input", commit},
		{"symbolic-ref", "HEAD", "refs/heads/input"},
		{"bundle", "create", "--quiet", bundle, "HEAD"},
	} {
		if _, err := gitOutput(cwd, append([]string{"--git-dir", tmp}, args...)...); err != nil {
			return err
		}
	}

	return nil
}

const (
	// lockFile is the file in the folder of a task that the task holds a
	// lock on while it runs
	lockFile = "lock"
	// newTaskPrefix starts the name of the folder of a task until it holds
	// its lock
	newTaskPrefix = ".new-"
)

// lockFlags open a lock file without following a link, without waiting
// for a FIFO and without making a terminal the controlling one.
const lockFlags = os.O_RDONLY | noFollowFlags

// lockTask opens the lock file in the folder of a task and locks it. Unless
// it waits, it fails with filelock.ErrLocked while the task runs.
func lockTask(dir string, flag int, wait bool) (*os.File, error) {
	file, err := openNoFollow(filepath.Join(dir, lockFile), flag|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}

	return lockOpened(file, wait)
}

// lockOpened locks the opened lock file, which must be a plain file, or
// closes it.
func lockOpened(file *os.File, wait bool) (*os.File, error) {
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = file.Close()

		return nil, errors.Join(errNoLockFile, err)
	}

	if err := filelock.Lock(file, wait); err != nil {
		_ = file.Close()

		return nil, err
	}

	return file, nil
}

// taskIDTime is the layout of the time a task ID starts with.
const taskIDTime = "20060102-150405"

// taskID is the time the task starts and a random part, so that tasks
// sort by their start and two that start at once differ.
func taskID() (string, error) {
	random := make([]byte, 3)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}

	return time.Now().Format(taskIDTime) + "-" + hex.EncodeToString(random), nil
}

func (t *taskRun) writers() map[string]io.Writer {
	writers := make(map[string]io.Writer, len(t.results))
	for name, file := range t.results {
		writers[name] = file
	}

	return writers
}

func (t *taskRun) close() {
	for _, file := range t.results {
		_ = file.Close()
	}

	if t.proxyLog != nil {
		_ = t.proxyLog.Close()
	}

	_ = t.lock.Close()
}

// report tells the person how the task went and where its results are,
// and returns an error when it did not go through.
func (t *taskRun) report(stdout, host, vm io.Writer) error {
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(host, format, args...) }

	content, err := readAll(t.results[task.ResultFile])
	if err != nil {
		return err
	}

	var result task.Result
	if err := json.Unmarshal(content, &result); err != nil {
		return fmt.Errorf("%w: %s: %w", task.ErrBadResults, task.ResultFile, err)
	}

	summary(host, vm, result)

	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintf(vm, "warning: %s\n", task.CleanLine(warning))
	}

	head, err := t.changes()

	// the VM said already when there are no changes
	switch {
	case err != nil:
		say("%v\n", err)
	case head == "":
	default:
		bundle := filepath.Join(t.dir, task.ChangesFile)
		branch := strings.TrimPrefix(task.Branch, "refs/heads/")
		say("the changes end at %s, fetch them with\n  git -c transfer.fsckObjects=true fetch %s %s:%s-%s\n", short(head), shellQuote(bundle), branch, branch, filepath.Base(t.dir))
	}

	// the folder goes to stdout alone, for scripts
	_, _ = fmt.Fprintln(stdout, t.dir)
	say("the results are in %s\n", t.dir)

	switch {
	case result.Error != "":
		return fmt.Errorf("the task failed: %s", task.CleanLine(result.Error))
	case err != nil:
		return err
	case result.TimedOut || result.ClaudeExitCode == nil || *result.ClaudeExitCode != 0:
		return errClaudeFailed
	}

	return nil
}

// changes checks the bundle of changes and returns the commit it ends at,
// or nothing when the task sent none.
func (t *taskRun) changes() (string, error) {
	file := t.results[task.ChangesFile]

	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return "", err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	bundle, err := task.ReadBundle(file)
	if err != nil {
		return "", err
	}

	return bundle.Check(t.base)
}

// summary tells how Claude Code ended, and what its last message says,
// cleaned, from the VM, since the task may have written it.
func summary(host, vm io.Writer, result task.Result) {
	if result.ClaudeExitCode != nil {
		line := fmt.Sprintf("Claude Code exited with %d", *result.ClaudeExitCode)

		if result.TimedOut {
			line += " after it ran out of time"
		}

		_, _ = fmt.Fprintln(host, line)
	}

	var last struct {
		Type     string  `json:"type"`
		Reason   string  `json:"terminal_reason"`
		Turns    int     `json:"num_turns"`
		Cost     float64 `json:"total_cost_usd"`
		Result   string  `json:"result"`
		Duration int     `json:"duration_ms"`
	}

	if json.Unmarshal(result.ClaudeResult, &last) != nil || last.Type != "result" {
		return
	}

	line := fmt.Sprintf("%d turns in %s, about %.2f USD at API prices", last.Turns, time.Duration(last.Duration)*time.Millisecond, last.Cost)

	if last.Reason != "" {
		line += ", ended by " + task.CleanLine(last.Reason)
	}

	_, _ = fmt.Fprintln(host, line)

	// the last message of Claude Code, which the task wrote
	if text := strings.TrimSpace(last.Result); text != "" {
		_, _ = fmt.Fprintln(vm, task.Indent(cut(text, 2000), "| "))
	}
}

func cut(text string, n int) string {
	if len(text) <= n {
		return text
	}

	return strings.ToValidUTF8(text[:n], "") + "..."
}

// shellQuote quotes the path for a shell, unless it needs no quotes.
func shellQuote(path string) string {
	if !strings.ContainsFunc(path, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-", r)
	}) {
		return path
	}

	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

func short(commit string) string {
	return commit[:min(len(commit), 12)]
}

func readAll(file *os.File) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	return io.ReadAll(io.LimitReader(file, task.MaxResultBytes))
}

// gitOutput runs git in the folder and returns what it printed, trimmed.
// The .git of the project is the VM's to write, so git runs no hooks and
// no fsmonitor from it. Filters it cannot turn off, so aibox runs no git
// command that reads the working tree, such as status.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)...) //nolint:gosec // the arguments are aibox's own
	cmd.Dir = dir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}

	return strings.TrimSpace(string(out)), nil
}
