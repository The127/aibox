//go:build linux

package guest

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/the127/aibox/internal/session"
	"github.com/the127/aibox/internal/task"
)

const (
	// the task share holds what the host gives a task
	taskShare  = "task"
	taskDir    = "/var/lib/aibox/task"
	taskPrompt = "/etc/aibox/task.md"
	gitPath    = "/usr/bin/git"

	// outDir is where the init keeps the results until it sends them. It is
	// on the state disk and only root may enter it.
	outDir = stateMount + "/out"

	maxTranscriptBytes = 256 << 20
	maxLogBytes        = 1 << 20
	maxChangesBytes    = 1 << 30
	maxGitBytes        = 64 << 10

	// aibox commits what the task left uncommitted under its own name
	leftoversIdentity = "aibox"
	leftoversEmail    = "aibox@localhost"
	leftoversMessage  = "aibox: what the task left uncommitted"
)

var (
	errNotACommit = errors.New("git did not print a commit")
	commitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// taskRun is a task on its way. Its commands run as the user, set up by
// command, and the init keeps their output in out, where the user cannot
// change it.
type taskRun struct {
	sys      Processes
	command  func(name string, args ...string) *exec.Cmd
	input    string
	out      string
	progress io.Writer
	// files are the complete results in out, in the order they go out
	files  []string
	result task.Result
}

func newTask(sys Processes, options Options, request session.Request) *taskRun {
	return &taskRun{
		sys: sys,
		command: func(name string, args ...string) *exec.Cmd {
			return userCommand(options, request, "dumb", name, args...)
		},
		input:    taskDir,
		out:      outDir,
		progress: io.Discard,
	}
}

// run runs the task and writes the results to w as a tar archive. It
// returns 0 when the task went through to its end, and 1 when a step
// failed, which result.json says.
func (t *taskRun) run(w io.Writer) int {
	if err := t.steps(); err != nil {
		t.result.Error = err.Error()
		t.say("aibox: %v\n", err)
	}

	if err := t.pack(w); err != nil {
		t.say("aibox: send the results: %v\n", err)

		return 1
	}

	if t.result.Error != "" {
		return 1
	}

	return 0
}

func (t *taskRun) steps() error {
	settings, err := readSettings(filepath.Join(t.input, task.SettingsFile))
	if err != nil {
		return err
	}

	if err := os.MkdirAll(t.out, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", t.out, err)
	}

	t.say("aibox: cloning the input\n")

	bundle := filepath.Join(t.input, task.InputBundle)
	if err := t.git(nil, "-c", "advice.detachedHead=false", "clone", "--quiet", "--", bundle, "."); err != nil {
		return fmt.Errorf("clone the input: %w", err)
	}

	branch := strings.TrimPrefix(task.Branch, "refs/heads/")
	if err := t.git(nil, "switch", "--quiet", "--create", branch); err != nil {
		return fmt.Errorf("make the branch %s: %w", branch, err)
	}

	base, err := t.commit("HEAD")
	if err != nil {
		return err
	}

	t.result.Base = base

	t.say("aibox: running Claude Code\n")

	code, err := t.claude(settings)
	if err != nil {
		return fmt.Errorf("run Claude Code: %w", err)
	}

	t.result.ClaudeExitCode = &code

	if err := t.commitLeftovers(); err != nil {
		warning := fmt.Sprintf("commit what the task left uncommitted: %v", err)
		t.result.Warnings = append(t.result.Warnings, warning)
		t.say("aibox: %s\n", warning)
	}

	// the task may have moved to another branch, and its last commit is
	// what it did
	if err := t.git(nil, "update-ref", task.Branch, "HEAD"); err != nil {
		return fmt.Errorf("point %s at the last commit: %w", task.Branch, err)
	}

	head, err := t.commit(task.Branch)
	if err != nil {
		return err
	}

	t.result.Head = head

	if head == base {
		t.say("aibox: the task made no changes\n")

		return nil
	}

	t.say("aibox: bundling the changes\n")

	return t.bundle(base)
}

// readSettings reads task.json. A field the VM does not know is an error,
// since the host would expect it to count.
func readSettings(path string) (task.Settings, error) {
	content, err := os.ReadFile(path) //nolint:gosec // the path is aibox's own
	if err != nil {
		return task.Settings{}, fmt.Errorf("read the settings of the task: %w", err)
	}

	var settings task.Settings

	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&settings); err != nil {
		return task.Settings{}, fmt.Errorf("read %s: %w: %w", task.SettingsFile, task.ErrBadSettings, err)
	}

	return settings, settings.Check()
}

// claude runs Claude Code on the prompt and keeps what it prints. Its exit
// code is part of the result, not a failure of the task.
func (t *taskRun) claude(settings task.Settings) (int, error) {
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
		"--append-system-prompt-file", taskPrompt,
	}

	if settings.Model != "" {
		args = append(args, "--model", settings.Model)
	}

	if settings.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(settings.MaxTurns))
	}

	if settings.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(settings.MaxBudgetUSD, 'f', -1, 64))
	}

	prompt, err := os.Open(filepath.Join(t.input, task.PromptFile))
	if err != nil {
		return 0, fmt.Errorf("open the prompt: %w", err)
	}

	defer func() { _ = prompt.Close() }()

	transcript, err := t.create(task.TranscriptFile, maxTranscriptBytes)
	if err != nil {
		return 0, err
	}

	defer transcript.close()

	log, err := t.create(task.LogFile, maxLogBytes)
	if err != nil {
		return 0, err
	}

	defer log.close()

	code, err := t.execute(t.command(claude, args...), prompt, transcript, log)
	if err != nil {
		return 0, err
	}

	if err := t.keep(transcript); err != nil {
		return 0, err
	}

	if err := t.keep(log); err != nil {
		return 0, err
	}

	return code, nil
}

// commitLeftovers commits what the task left in the working tree, so that
// the bundle carries it.
func (t *taskRun) commitLeftovers() error {
	var status bytes.Buffer

	if err := t.git(&limited{w: &status, left: maxGitBytes}, "status", "--porcelain", "--untracked-files=all"); err != nil {
		return err
	}

	if status.Len() == 0 {
		return nil
	}

	t.say("aibox: committing what the task left uncommitted\n")

	if err := t.git(nil, "add", "--all"); err != nil {
		return err
	}

	// the variables win over any identity the task set up
	commit := t.command(gitPath, "-c", "commit.gpgSign=false", "commit", "--quiet", "--no-verify", "--message", leftoversMessage)
	commit.Env = append(commit.Env,
		"GIT_AUTHOR_NAME="+leftoversIdentity,
		"GIT_AUTHOR_EMAIL="+leftoversEmail,
		"GIT_COMMITTER_NAME="+leftoversIdentity,
		"GIT_COMMITTER_EMAIL="+leftoversEmail,
	)

	if err := t.runGit(commit, nil); err != nil {
		return err
	}

	t.result.Leftovers = true

	return nil
}

// bundle keeps a git bundle of the branch from the base on.
func (t *taskRun) bundle(base string) error {
	changes, err := t.create(task.ChangesFile, maxChangesBytes)
	if err != nil {
		return err
	}

	defer changes.close()

	if err := t.git(changes, "bundle", "create", "--quiet", "-", base+".."+task.Branch); err != nil {
		return fmt.Errorf("bundle the changes: %w", err)
	}

	if changes.cut {
		return fmt.Errorf("the changes are larger than %d MiB", maxChangesBytes>>20)
	}

	return t.keep(changes)
}

// commit returns the commit git names for the revision.
func (t *taskRun) commit(revision string) (string, error) {
	var out bytes.Buffer

	if err := t.git(&limited{w: &out, left: maxGitBytes}, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}"); err != nil {
		return "", fmt.Errorf("find the commit of %s: %w", revision, err)
	}

	commit := strings.TrimSpace(out.String())
	if !commitPattern.MatchString(commit) {
		return "", fmt.Errorf("find the commit of %s: %w: %q", revision, errNotACommit, commit)
	}

	return commit, nil
}

// git runs git as the user in the project, with what it prints going to
// stdout.
func (t *taskRun) git(stdout io.Writer, args ...string) error {
	return t.runGit(t.command(gitPath, args...), stdout)
}

// runGit runs a git command. When it fails, the error says what git printed
// to standard error.
func (t *taskRun) runGit(cmd *exec.Cmd, stdout io.Writer) error {
	var stderr bytes.Buffer

	code, err := t.execute(cmd, nil, stdout, &limited{w: &stderr, left: maxGitBytes})
	if err != nil {
		return err
	}

	if code != 0 {
		return fmt.Errorf("git exited with %d: %s", code, strings.TrimSpace(stderr.String()))
	}

	return nil
}

// execute runs the command until it exits, with stdin as its input and what
// it prints going to stdout and stderr, any of them nil for none. What the
// command left running is killed before its output counts as complete, so
// that nothing the user runs outlives a step.
func (t *taskRun) execute(cmd *exec.Cmd, stdin *os.File, stdout, stderr io.Writer) (int, error) {
	if stdin != nil {
		cmd.Stdin = stdin
	}

	var (
		copies sync.WaitGroup
		ends   []*os.File
	)

	closeEnds := func() {
		for _, end := range ends {
			_ = end.Close()
		}
	}

	for _, output := range []struct {
		to  io.Writer
		set *io.Writer
	}{{stdout, &cmd.Stdout}, {stderr, &cmd.Stderr}} {
		if output.to == nil {
			continue
		}

		r, w, err := os.Pipe()
		if err != nil {
			closeEnds()
			copies.Wait()

			return 0, fmt.Errorf("make a pipe: %w", err)
		}

		*output.set = w
		ends = append(ends, w)

		copies.Go(func() {
			_, _ = io.Copy(output.to, r)
			_ = r.Close()
		})
	}

	pid, err := t.sys.Start(cmd, sessionCgroup)

	// the command has its own copies of these now
	closeEnds()

	if err != nil {
		copies.Wait()

		return 0, fmt.Errorf("start %s: %w", cmd.Path, err)
	}

	code, err := reap(t.sys, pid)
	if err != nil {
		return 0, fmt.Errorf("wait for %s: %w", cmd.Path, err)
	}

	if err := t.sys.Kill(userCgroup); err != nil {
		return 0, fmt.Errorf("end what %s left running: %w", cmd.Path, err)
	}

	copies.Wait()

	return code, nil
}

// output is a result on its way into out.
type output struct {
	limited

	name string
	file *os.File
}

// create makes the file of a result, which keeps up to max bytes.
func (t *taskRun) create(name string, maxBytes int64) (*output, error) {
	path := filepath.Join(t.out, name)

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // the path is aibox's own
	if err != nil {
		return nil, fmt.Errorf("make %s: %w", path, err)
	}

	return &output{limited: limited{w: file, left: maxBytes}, name: name, file: file}, nil
}

func (o *output) close() { _ = o.file.Close() }

// keep makes the result one that goes out.
func (t *taskRun) keep(o *output) error {
	err := o.file.Close()
	if o.err != nil {
		err = o.err
	}

	if err != nil {
		return fmt.Errorf("write %s: %w", o.name, err)
	}

	if o.cut {
		t.result.Truncated = append(t.result.Truncated, o.name)
	}

	t.files = append(t.files, o.name)

	return nil
}

// pack writes the results as a tar archive, result.json last.
func (t *taskRun) pack(w io.Writer) error {
	archive := tar.NewWriter(w)

	for _, name := range t.files {
		if err := addFile(archive, filepath.Join(t.out, name), name); err != nil {
			return err
		}
	}

	result, err := json.Marshal(t.result)
	if err != nil {
		return err
	}

	header := &tar.Header{Typeflag: tar.TypeReg, Name: task.ResultFile, Mode: 0o644, Size: int64(len(result))}
	if err := archive.WriteHeader(header); err != nil {
		return err
	}

	if _, err := archive.Write(result); err != nil {
		return err
	}

	return archive.Close()
}

func addFile(archive *tar.Writer, path, name string) error {
	file, err := os.Open(path) //nolint:gosec // the path is aibox's own
	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: info.Size()}
	if err := archive.WriteHeader(header); err != nil {
		return err
	}

	_, err = io.Copy(archive, file)

	return err
}

func (t *taskRun) say(format string, args ...any) {
	say(t.progress, format, args...)
}

// limited passes the first bytes on, up to what is left, and drops the
// rest. It never fails, so that a command that prints more does not stop on
// a full pipe. The first error of the writer below is kept for later.
type limited struct {
	w    io.Writer
	left int64
	cut  bool
	err  error
}

func (l *limited) Write(b []byte) (int, error) {
	n := len(b)

	if int64(len(b)) > l.left {
		b = b[:l.left]
		l.cut = true
	}

	l.left -= int64(len(b))

	if len(b) > 0 && l.err == nil {
		_, l.err = l.w.Write(b)
	}

	return n, nil
}

// taskProcess is a task running in the init. Reads return the results as a
// tar archive, and standard error says what the task is doing.
type taskProcess struct {
	results, progress *io.PipeReader
	done              chan struct{}
	code              int
}

func startTask(t *taskRun) *taskProcess {
	results, resultsWriter := io.Pipe()
	progress, progressWriter := io.Pipe()

	p := &taskProcess{results: results, progress: progress, done: make(chan struct{})}
	t.progress = progressWriter

	go func() {
		defer close(p.done)

		p.code = t.run(resultsWriter)

		_ = resultsWriter.Close()
		_ = progressWriter.Close()
	}()

	return p
}

func (p *taskProcess) Read(b []byte) (int, error) { return p.results.Read(b) }

// Write drops the input, since a task reads none.
func (p *taskProcess) Write(b []byte) (int, error) { return len(b), nil }

func (p *taskProcess) Stderr() io.Reader { return p.progress }

func (p *taskProcess) CloseInput() error { return nil }

// Resize does nothing, since there is no terminal to resize.
func (p *taskProcess) Resize(session.Size) error { return nil }

func (p *taskProcess) Wait() (int, error) {
	<-p.done

	return p.code, nil
}

// Close ends the reads, which also stops a task whose results nobody takes.
func (p *taskProcess) Close() error {
	_ = p.progress.Close()

	return p.results.Close()
}
