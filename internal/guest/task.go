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
	"time"
	"unicode"

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
	// on the state disk and only root may enter it. ext4 keeps blocks for
	// root, so the results still fit when the task has filled the disk.
	outDir = stateMount + "/out"

	maxTranscriptBytes = 256 << 20
	maxLogBytes        = 1 << 20
	maxChangesBytes    = 1 << 30
	maxGitBytes        = 64 << 10
	maxResultBytes     = 64 << 10
	// what git printed goes into the result and to the terminal of the
	// person, cut to this
	maxMessageBytes = 2 << 10

	// gitTimeout is how long the git steps before Claude Code may take
	// together, and again the ones after it. The task could make them
	// slow with its own git config.
	gitTimeout = 10 * time.Minute

	// drainTimeout is how long the output of a step is read after what the
	// step left running could not be ended, which may hold the pipes open
	drainTimeout = time.Second

	// aibox commits what the task left uncommitted under its own name, and
	// the commits of the task too when the host gave no identity
	aiboxName        = "aibox"
	aiboxEmail       = "aibox@localhost"
	leftoversMessage = "aibox: what the task left uncommitted"
)

var (
	errNotACommit  = errors.New("git did not print a commit")
	errLeftRunning = errors.New("what the step left running could not be ended")
	errTrailing    = errors.New("data after the settings")
	errGitTime     = errors.New("the git steps took too long")
	commitPattern  = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// taskRun is a task on its way. Its commands run as the user, set up by
// command, and the init keeps their output in out, where the user cannot
// change it.
type taskRun struct {
	sys        Processes
	command    func(name string, args ...string) *exec.Cmd
	input      string
	out        string
	progress   io.Writer
	gitTimeout time.Duration
	// gitDeadline is when the git steps of the current phase must be done
	gitDeadline time.Time
	// archive is where the results go out, each as soon as it is complete
	archive *tar.Writer
	result  task.Result
}

func newTask(sys Processes, options Options, request session.Request) *taskRun {
	return &taskRun{
		sys: sys,
		command: func(name string, args ...string) *exec.Cmd {
			return userCommand(options, request, "dumb", name, args...)
		},
		input:      taskDir,
		out:        outDir,
		progress:   io.Discard,
		gitTimeout: gitTimeout,
	}
}

// run runs the task and writes the results to w as a tar archive, each
// file as soon as it is complete, so that what the task did is out before
// the steps that come after it. It returns 0 when the task went through to
// its end, and 1 when a step failed, which result.json says.
func (t *taskRun) run(w io.Writer) int {
	t.archive = tar.NewWriter(w)

	if err := t.steps(); err != nil {
		t.result.Error = err.Error()
		t.say("aibox: %v\n", err)
	}

	if err := t.finish(); err != nil {
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

	t.gitDeadline = time.Now().Add(t.gitTimeout)

	if err := t.setIdentity(settings); err != nil {
		return fmt.Errorf("set the git identity: %w", err)
	}

	t.say("aibox: cloning the input\n")

	bundle := filepath.Join(t.input, task.InputBundle)
	if err := t.git(nil, "-c", "advice.detachedHead=false", "clone", "--quiet", "--", bundle, "."); err != nil {
		return fmt.Errorf("clone the input: %w", err)
	}

	// the bundle is no remote the task can use
	if err := t.git(nil, "remote", "remove", "origin"); err != nil {
		return fmt.Errorf("remove the remote of the input: %w", err)
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

	if err := t.claude(settings); err != nil {
		return fmt.Errorf("run Claude Code: %w", err)
	}

	t.gitDeadline = time.Now().Add(t.gitTimeout)

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

// setIdentity puts the git identity of the task into the git config of the
// home.
func (t *taskRun) setIdentity(settings task.Settings) error {
	name, email := settings.GitName, settings.GitEmail
	if name == "" {
		name = aiboxName
	}

	if email == "" {
		email = aiboxEmail
	}

	if err := t.git(nil, "config", "--global", "user.name", name); err != nil {
		return err
	}

	return t.git(nil, "config", "--global", "user.email", email)
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

	if decoder.More() {
		return task.Settings{}, fmt.Errorf("read %s: %w: %w", task.SettingsFile, task.ErrBadSettings, errTrailing)
	}

	return settings, settings.Check()
}

// claude runs Claude Code on the prompt and keeps what it prints, up to
// the timeout of the settings. Its exit code is part of the result, not a
// failure of the task.
func (t *taskRun) claude(settings task.Settings) error {
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
		return fmt.Errorf("open the prompt: %w", err)
	}

	defer func() { _ = prompt.Close() }()

	transcript, err := t.create(task.TranscriptFile, maxTranscriptBytes)
	if err != nil {
		return err
	}

	defer transcript.close()

	log, err := t.create(task.LogFile, maxLogBytes)
	if err != nil {
		return err
	}

	defer log.close()

	var last lastLine

	run := &step{
		cmd:     t.command(claude, args...),
		cgroup:  sessionCgroup,
		timeout: time.Duration(settings.TimeoutSeconds) * time.Second,
		stdin:   prompt,
		stdout:  io.MultiWriter(transcript, &last),
		stderr:  log,
	}

	// what Claude Code printed goes out even when what it left running
	// could not be ended
	code, err := t.execute(run)
	if err != nil && !errors.Is(err, errLeftRunning) {
		return err
	}

	t.result.ClaudeExitCode = &code
	t.result.ClaudeResult = last.json()
	t.result.TimedOut = run.timedOut

	if t.result.TimedOut {
		t.say("aibox: Claude Code ran out of time\n")
	}

	for _, o := range []*output{transcript, log} {
		if err := t.keep(o); err != nil {
			return err
		}
	}

	return err
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
	commit := t.command(gitPath, gitArgs("-c", "commit.gpgSign=false", "commit", "--quiet", "--no-verify", "--message", leftoversMessage)...)
	commit.Env = append(commit.Env,
		"GIT_AUTHOR_NAME="+aiboxName,
		"GIT_AUTHOR_EMAIL="+aiboxEmail,
		"GIT_COMMITTER_NAME="+aiboxName,
		"GIT_COMMITTER_EMAIL="+aiboxEmail,
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
	return t.runGit(t.command(gitPath, gitArgs(args...)...), stdout)
}

// gitArgs turns off what the git config of the task could make git run:
// hooks and a file system monitor. Filters of the task still run, within
// the deadline.
func gitArgs(args ...string) []string {
	return append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
}

// runGit runs a git command in the cgroup of aibox. When it fails, the
// error says what git printed to standard error.
func (t *taskRun) runGit(cmd *exec.Cmd, stdout io.Writer) error {
	left := time.Until(t.gitDeadline)
	if left <= 0 {
		return fmt.Errorf("%w: %v", errGitTime, t.gitTimeout)
	}

	var stderr bytes.Buffer

	run := &step{
		cmd:     cmd,
		cgroup:  stepCgroup,
		timeout: left,
		stdout:  stdout,
		stderr:  &limited{w: &stderr, left: maxGitBytes},
	}

	code, err := t.execute(run)
	if err != nil {
		return err
	}

	if run.timedOut {
		return fmt.Errorf("%w: %v", errGitTime, t.gitTimeout)
	}

	if code != 0 {
		return fmt.Errorf("git exited with %d: %s", code, message(stderr.String()))
	}

	return nil
}

// message makes what a program of the task printed safe to show on one
// line: the lines are joined with " | ", other control and format
// characters become ?, and a long text is cut.
func message(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > maxMessageBytes {
		text = text[:maxMessageBytes] + "..."
	}

	var lines []string

	for line := range strings.SplitSeq(strings.ToValidUTF8(text, "?"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		lines = append(lines, strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}

			if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || r == unicode.ReplacementChar {
				return '?'
			}

			return r
		}, line))
	}

	return strings.Join(lines, " | ")
}

// killedCode is the exit code a SIGKILL gives, from Wait of the System.
const killedCode = 128 + int(syscall.SIGKILL)

// step is a command of the task: the cgroup it runs in, how long it may
// take, if there is a limit, and where its input comes from and its output
// goes, nil for none. timedOut says whether the limit ended it.
type step struct {
	cmd            *exec.Cmd
	cgroup         string
	timeout        time.Duration
	stdin          *os.File
	stdout, stderr io.Writer

	mu       sync.Mutex
	finished bool
	timedOut bool
}

// expire ends the step when its time is up. A step that has finished is
// left alone, so that a late timer does not hit the next step.
func (s *step) expire(endAll func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.finished {
		return
	}

	s.timedOut = true
	_ = endAll()
}

// finish marks the step as finished with the exit code, once a running
// expire is done. A limit that came only after the command had exited by
// itself did not end it.
func (s *step) finish(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.finished = true
	s.timedOut = s.timedOut && code == killedCode
}

// execute runs the step until it exits and returns its exit code. What the
// step left running is killed before its output counts as complete, so
// that nothing the user runs outlives a step. When that fails, the error
// wraps errLeftRunning, and the output is what came within drainTimeout.
func (t *taskRun) execute(s *step) (int, error) {
	cmd := s.cmd
	if s.stdin != nil {
		cmd.Stdin = s.stdin
	}

	var (
		copies  sync.WaitGroup
		readers []*os.File
		ends    []*os.File
	)

	closeEnds := func() {
		for _, end := range ends {
			_ = end.Close()
		}
	}

	for _, output := range []struct {
		to  io.Writer
		set *io.Writer
	}{{s.stdout, &cmd.Stdout}, {s.stderr, &cmd.Stderr}} {
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
		readers = append(readers, r)

		copies.Go(func() {
			_, _ = io.Copy(output.to, r)
			_ = r.Close()
		})
	}

	pid, err := t.sys.Start(cmd, s.cgroup)

	// the command has its own copies of these now
	closeEnds()

	if err != nil {
		copies.Wait()

		return 0, fmt.Errorf("start %s: %w", cmd.Path, err)
	}

	if s.timeout > 0 {
		// the user may have moved out of the cgroup of the step
		timer := time.AfterFunc(s.timeout, func() { s.expire(t.endAll) })
		defer timer.Stop()
	}

	code, waitErr := reap(t.sys, pid)
	s.finish(code)

	if err := t.endAll(); err != nil || waitErr != nil {
		// a process that is left may hold the pipes open
		for _, r := range readers {
			_ = r.SetReadDeadline(time.Now().Add(drainTimeout))
		}

		copies.Wait()

		if waitErr != nil {
			return 0, fmt.Errorf("wait for %s: %w", cmd.Path, waitErr)
		}

		return code, fmt.Errorf("%w: %w", errLeftRunning, err)
	}

	copies.Wait()

	return code, nil
}

// endAll kills every process of the user and of the steps of aibox.
func (t *taskRun) endAll() error {
	return errors.Join(t.sys.Kill(userCgroup), t.sys.Kill(stepCgroup))
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

// keep sends the result out.
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

	if err := addFile(t.archive, filepath.Join(t.out, o.name), o.name); err != nil {
		return fmt.Errorf("send %s: %w", o.name, err)
	}

	return nil
}

// finish sends result.json and ends the archive.
func (t *taskRun) finish() error {
	archive := t.archive

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

// lastLine keeps the last line written to it that is not empty, if it is
// not longer than maxResultBytes.
type lastLine struct {
	line, last []byte
	long       bool
}

func (l *lastLine) Write(b []byte) (int, error) {
	n := len(b)

	for len(b) > 0 {
		part, rest, ended := bytes.Cut(b, []byte("\n"))

		if len(l.line)+len(part) > maxResultBytes {
			l.long, l.line = true, l.line[:0]
		} else if !l.long {
			l.line = append(l.line, part...)
		}

		if !ended {
			break
		}

		l.end()
		b = rest
	}

	return n, nil
}

// end ends the current line. A line that is too long leaves no last line,
// so that the one before it is not taken for the last.
func (l *lastLine) end() {
	switch line := bytes.TrimSpace(l.line); {
	case l.long:
		l.last = nil
	case len(line) > 0:
		l.last = bytes.Clone(line)
	}

	l.line, l.long = l.line[:0], false
}

// json returns the last line when it is a JSON object. A line without its
// newline at the end counts.
func (l *lastLine) json() json.RawMessage {
	if l.long || len(bytes.TrimSpace(l.line)) > 0 {
		l.end()
	}

	if !bytes.HasPrefix(l.last, []byte("{")) || !json.Valid(l.last) {
		return nil
	}

	return l.last
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

// Close ends the reads. The task goes on until the VM halts, but it can no
// longer send anything.
func (p *taskProcess) Close() error {
	_ = p.progress.Close()

	return p.results.Close()
}
