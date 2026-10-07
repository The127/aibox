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

	"github.com/urfave/cli/v3"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/task"
)

// maxTimeout is the longest --timeout, far beyond any task, so that the
// time to stop the VM cannot overflow.
const maxTimeout = 30 * 24 * time.Hour

var (
	errNoPrompt     = errors.New("task needs a prompt, as its arguments")
	errClaudeFailed = errors.New("the task did not finish, see the transcript")
	errNoCommit     = errors.New("task must start in a git repository with a commit")
	errNoTimeout    = errors.New("--timeout must be at least a second and at most 30 days")
)

// taskSlack is how much longer than its timeout a task may take before
// aibox stops the VM: the boot and the git steps before and after Claude
// Code, which have 10 minutes each in the VM.
const taskSlack = 25 * time.Minute

// credentials are the variables Claude Code logs in with. A task starts
// with an empty home, so one of them must reach the VM.
var credentials = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

func taskCommand(deps dependencies) *cli.Command {
	return &cli.Command{
		Name:      "task",
		Usage:     "run Claude Code unattended on the last commit, in a VM of its own, and keep its commits as a git bundle",
		ArgsUsage: "PROMPT",
		Flags: append(vmFlags(),
			&cli.StringFlag{Name: "model", Usage: "the model Claude Code uses", DefaultText: "the default of Claude Code"},
			&cli.IntFlag{Name: "max-turns", Usage: "the most turns Claude Code takes", DefaultText: "no limit"},
			&cli.FloatFlag{Name: "max-budget-usd", Usage: "the most Claude Code may spend, in US dollars", DefaultText: "no limit"},
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
}

func runTask(ctx context.Context, deps dependencies, cmd *cli.Command) error {
	if deps.uid() == 0 {
		return errRoot
	}

	prompt := strings.TrimSpace(strings.Join(cmd.Args().Slice(), " "))
	if prompt == "" {
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

	base, err := gitOutput(cwd, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("%w: %w", errNoCommit, err)
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

	warnings(deps.stderr, cwd, r.env)

	t, err := newTaskRun(r.project.Dir, cwd, base, prompt, settings)
	if err != nil {
		return err
	}

	defer t.close()

	_, _ = fmt.Fprintf(deps.stderr, "aibox: task %s starts from %s, booting the VM\n", filepath.Base(t.dir), short(base))

	spec := r.spec(cmd)
	spec.Stderr = deps.stderr
	spec.State = filepath.Join(t.dir, "state.ext4")
	spec.RemoveState = true
	spec.Task = t.share
	spec.ConsoleLog = filepath.Join(t.dir, "console.log")

	progress := task.NewLineCleaner(deps.stderr)
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

	// aibox can remove it until it is confined, and the backend removes it
	// once the VM has it, so it is gone either way
	_ = os.Remove(spec.State)

	if err := outcome(ctx, runErr, receiveErr, limit); err != nil {
		_, _ = fmt.Fprintln(deps.stdout, t.dir)
		_, _ = fmt.Fprintf(deps.stderr, "aibox: what the task left is in %s\n", t.dir)

		return err
	}

	return t.report(deps.stdout, deps.stderr)
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

// warnings tells the person what the task will not have: the changes not
// yet committed, and a login when the config passes no credential.
func warnings(stderr io.Writer, cwd string, env []string) {
	if status, err := gitOutput(cwd, "status", "--porcelain"); err == nil && status != "" {
		_, _ = fmt.Fprintln(stderr, "aibox: the task starts from the last commit, changes not committed are not part of it")
	}

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

	t := &taskRun{
		dir:     filepath.Join(projectDir, "tasks", id),
		base:    base,
		results: map[string]*os.File{},
	}
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
	if _, err := gitOutput(cwd, "bundle", "create", "--quiet", bundle, "HEAD"); err != nil {
		return nil, fmt.Errorf("bundle the last commit: %w", err)
	}

	if err := os.Chmod(bundle, 0o644); err != nil { //nolint:gosec // the VM reads it
		return nil, err
	}

	for _, name := range []string{task.TranscriptFile, task.LogFile, task.ChangesFile, task.ResultFile} {
		file, err := os.OpenFile(filepath.Join(t.dir, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the names are aibox's own
		if err != nil {
			t.close()

			return nil, fmt.Errorf("make %s: %w", name, err)
		}

		t.results[name] = file
	}

	return t, nil
}

// taskID is the time the task starts and a random part, so that tasks
// sort by their start and two that start at once differ.
func taskID() (string, error) {
	random := make([]byte, 3)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}

	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(random), nil
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
}

// report tells the person how the task went and where its results are,
// and returns an error when it did not go through.
func (t *taskRun) report(stdout, stderr io.Writer) error {
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(stderr, format, args...) }

	content, err := readAll(t.results[task.ResultFile])
	if err != nil {
		return err
	}

	var result task.Result
	if err := json.Unmarshal(content, &result); err != nil {
		return fmt.Errorf("%w: %s: %w", task.ErrBadResults, task.ResultFile, err)
	}

	say("%s", summary(result))

	for _, warning := range result.Warnings {
		say("aibox: warning: %s\n", task.CleanLine(warning))
	}

	head, err := t.changes()

	// the VM said already when there are no changes
	switch {
	case err != nil:
		say("aibox: %v\n", err)
	case head == "":
	default:
		bundle := filepath.Join(t.dir, task.ChangesFile)
		branch := strings.TrimPrefix(task.Branch, "refs/heads/")
		say("aibox: the changes end at %s, fetch them with\n  git -c transfer.fsckObjects=true fetch %s %s:%s-%s\n", short(head), shellQuote(bundle), branch, branch, filepath.Base(t.dir))
	}

	// the folder goes to stdout alone, for scripts
	_, _ = fmt.Fprintln(stdout, t.dir)
	say("aibox: the results are in %s\n", t.dir)

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

// summary is what the last line of Claude Code says, cleaned, since the
// task may have written it.
func summary(result task.Result) string {
	var b strings.Builder

	if result.ClaudeExitCode != nil {
		fmt.Fprintf(&b, "aibox: Claude Code exited with %d", *result.ClaudeExitCode)

		if result.TimedOut {
			b.WriteString(" after it ran out of time")
		}

		b.WriteString("\n")
	}

	var last struct {
		Type     string  `json:"type"`
		Reason   string  `json:"terminal_reason"`
		Turns    int     `json:"num_turns"`
		Cost     float64 `json:"total_cost_usd"`
		Result   string  `json:"result"`
		Duration int     `json:"duration_ms"`
	}

	if json.Unmarshal(result.ClaudeResult, &last) == nil && last.Type == "result" {
		fmt.Fprintf(&b, "aibox: %d turns in %s for %.2f USD", last.Turns, time.Duration(last.Duration)*time.Millisecond, last.Cost)

		if last.Reason != "" {
			fmt.Fprintf(&b, ", ended by %s", task.CleanLine(last.Reason))
		}

		b.WriteString("\n")

		// the last message of Claude Code, which the task wrote
		if text := strings.TrimSpace(last.Result); text != "" {
			fmt.Fprintf(&b, "%s\n", task.Indent(cut(text, 2000), "  | "))
		}
	}

	return b.String()
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
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...) //nolint:gosec // the arguments are aibox's own
	cmd.Dir = dir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}

	return strings.TrimSpace(string(out)), nil
}
