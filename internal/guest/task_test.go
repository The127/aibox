//go:build linux

package guest_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/guest"
	"github.com/the127/aibox/internal/session"
	"github.com/the127/aibox/internal/task"
)

// results are what a task sent back.
type results struct {
	names  []string
	files  map[string][]byte
	result task.Result
}

func readResults(t *testing.T, r io.Reader) results {
	t.Helper()

	got := results{files: map[string][]byte{}}
	archive := tar.NewReader(r)

	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		require.NoError(t, err)

		content, err := io.ReadAll(archive)
		require.NoError(t, err)

		got.names = append(got.names, header.Name)
		got.files[header.Name] = content
	}

	require.Contains(t, got.files, task.ResultFile)
	require.NoError(t, json.Unmarshal(got.files[task.ResultFile], &got.result))

	return got
}

// taskProcesses runs the commands of a task for real, as the user of the
// test, one after the other. Each runs in a process group of its own, which
// a kill ends in place of the cgroups.
type taskProcesses struct {
	mu      sync.Mutex
	running *exec.Cmd
	started []startedIn
	kills   []string
	// made are the cgroups made, delegated the ones given to the user
	made      []string
	delegated []string
	// failKill fails every kill, failKillAfter the kills after the command
	// of that path
	failKill      error
	failKillAfter string
}

type startedIn struct {
	cmd    *exec.Cmd
	cgroup string
}

func (p *taskProcesses) Start(cmd *exec.Cmd, cgroup string) (int, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.running = cmd
	p.started = append(p.started, startedIn{cmd, cgroup})

	return cmd.Process.Pid, nil
}

func (p *taskProcesses) Wait() (int, int, error) {
	p.mu.Lock()
	running := p.running
	p.mu.Unlock()

	err := running.Wait()

	// a signal gives 128 plus its number, like Wait of the System
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return running.Process.Pid, 128 + int(status.Signal()), nil
		}

		return running.Process.Pid, exit.ExitCode(), nil
	}

	return running.Process.Pid, 0, err
}

func (p *taskProcesses) Kill(cgroup string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.kills = append(p.kills, cgroup)

	if p.failKill != nil {
		return p.failKill
	}

	if p.failKillAfter != "" && p.running.Path == p.failKillAfter {
		return errors.New("still populated")
	}

	for _, s := range p.started {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	}

	return nil
}

func (p *taskProcesses) Mkdir(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.made = append(p.made, path)

	return nil
}

func (p *taskProcesses) Delegate(cgroup string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.made = append(p.made, cgroup)
	p.delegated = append(p.delegated, cgroup)

	return nil
}

func (p *taskProcesses) startedCopy() []startedIn {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.started)
}

// claudeCalls are the commands that ran Claude Code.
func (p *taskProcesses) claudeCalls(claude string) []*exec.Cmd {
	var calls []*exec.Cmd

	for _, s := range p.startedCopy() {
		if s.cmd.Path == claude {
			calls = append(calls, s.cmd)
		}
	}

	return calls
}

// taskFixture is a task share with an input bundle of a repository with one
// commit, a project folder to clone it into and a script in place of
// Claude Code.
type taskFixture struct {
	t         *testing.T
	source    string
	input     string
	project   string
	out       string
	home      string
	claude    string
	base      string
	processes *taskProcesses
	progress  bytes.Buffer
	// gitTimeout is how long the git steps before and after Claude Code
	// may take
	gitTimeout time.Duration
}

func newTaskFixture(t *testing.T, script string) *taskFixture {
	t.Helper()

	dir := t.TempDir()
	f := &taskFixture{
		t:          t,
		source:     filepath.Join(dir, "source"),
		input:      filepath.Join(dir, "input"),
		project:    filepath.Join(dir, "project"),
		out:        filepath.Join(dir, "out"),
		home:       filepath.Join(dir, "home"),
		claude:     filepath.Join(dir, "claude"),
		processes:  &taskProcesses{},
		gitTimeout: time.Minute,
	}

	for _, d := range []string{f.source, f.input, f.project, f.home} {
		require.NoError(t, os.Mkdir(d, 0o750))
	}

	f.gitIn(f.source, "init", "--quiet", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(f.source, "README"), []byte("hello\n"), 0o600))
	f.gitIn(f.source, "add", "README")
	f.gitIn(f.source, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--message", "one")
	f.gitIn(f.source, "bundle", "create", "--quiet", filepath.Join(f.input, task.InputBundle), "HEAD")
	f.base = strings.TrimSpace(f.gitIn(f.source, "rev-parse", "HEAD"))

	f.writeInput(task.SettingsFile, "{}")
	f.writeInput(task.PromptFile, "Fix the README.\n")
	require.NoError(t, os.WriteFile(f.claude, []byte("#!/bin/sh\nset -e\n"+script), 0o755)) //nolint:gosec // the script must run

	return f
}

func (f *taskFixture) writeInput(name, content string) {
	require.NoError(f.t, os.WriteFile(filepath.Join(f.input, name), []byte(content), 0o600))
}

// env keeps git away from the configuration of the machine. The task has
// a home of its own.
func (f *taskFixture) env(home string) []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "XDG_CONFIG_HOME="+home, "HOME="+home)
}

func (f *taskFixture) gitIn(dir string, args ...string) string {
	cmd := exec.Command("git", args...) //nolint:gosec // the arguments are the test's own
	cmd.Dir = dir
	cmd.Env = append(f.env(f.t.TempDir()), "GIT_CONFIG_GLOBAL=/dev/null")

	out, err := cmd.CombinedOutput()
	require.NoError(f.t, err, string(out))

	return string(out)
}

func (f *taskFixture) command(name string, args ...string) *exec.Cmd {
	switch name {
	case guest.ClaudePath:
		name = f.claude
	case guest.GitPath:
		name = "git"
	}

	cmd := exec.Command(name, args...) //nolint:gosec // the programs are the test's own
	cmd.Dir = f.project
	cmd.Env = f.env(f.home)

	return cmd
}

func (f *taskFixture) run() (int, results) {
	var out bytes.Buffer

	code := guest.RunTask(f.processes, f.command, f.input, f.out, f.gitTimeout, &out, &f.progress)

	return code, readResults(f.t, &out)
}

// fetch fetches the branch of the changes into the source repository and
// returns the subjects and authors of its commits from the base on.
func (f *taskFixture) fetch(r results) string {
	bundle := filepath.Join(f.t.TempDir(), task.ChangesFile)
	require.NoError(f.t, os.WriteFile(bundle, r.files[task.ChangesFile], 0o600))
	f.gitIn(f.source, "fetch", "--quiet", bundle, task.Branch+":refs/heads/changes")

	return f.gitIn(f.source, "log", "--format=%s by %an", f.base+"..changes")
}

func TestTaskSendsTheCommitsOfClaudeCodeAsABundle(t *testing.T) {
	// arrange
	f := newTaskFixture(t, `
echo fixed > README
git add README
git -c user.name=Claude -c user.email=claude@example.com commit --quiet --message "fix the README"
`)

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, []string{"transcript.jsonl", "claude.log", "changes.bundle", "result.json"}, r.names)
	assert.Equal(t, f.base, r.result.Base)
	assert.NotEqual(t, f.base, r.result.Head)
	assert.Equal(t, 0, *r.result.ClaudeExitCode)
	assert.False(t, r.result.Leftovers)
	assert.Empty(t, r.result.Error)
	assert.Equal(t, "fix the README by Claude\n", f.fetch(r))
	assert.Equal(t, "fixed\n", f.gitIn(f.source, "show", "changes:README"))
}

func TestTaskCommitsWhatClaudeCodeLeftUncommitted(t *testing.T) {
	// arrange
	f := newTaskFixture(t, `
echo fixed > README
mkdir docs
echo new > docs/guide
`)

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	assert.True(t, r.result.Leftovers)
	assert.Equal(t, "aibox: what the task left uncommitted by aibox\n", f.fetch(r))
	assert.Equal(t, "new\n", f.gitIn(f.source, "show", "changes:docs/guide"))
}

func TestTaskSendsTheBranchClaudeCodeEndedOn(t *testing.T) {
	// arrange
	f := newTaskFixture(t, `
git switch --quiet --create elsewhere
git -c user.name=Claude -c user.email=claude@example.com commit --quiet --allow-empty --message "on another branch"
`)

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, "on another branch by Claude\n", f.fetch(r))
}

func TestTaskWithoutChangesSendsNoBundle(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "")

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, []string{"transcript.jsonl", "claude.log", "result.json"}, r.names)
	assert.Equal(t, f.base, r.result.Head)
	assert.Contains(t, f.progress.String(), "the task made no changes")
}

func TestTaskRunsClaudeCodeUnattendedOnThePrompt(t *testing.T) {
	// arrange
	f := newTaskFixture(t, `
cat
echo '{"type":"result"}'
echo warning >&2
`)
	f.writeInput(task.SettingsFile, `{"model":"opus","maxTurns":3,"maxBudgetUSD":1.5}`)

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)

	calls := f.processes.claudeCalls(f.claude)
	require.Len(t, calls, 1)
	assert.Equal(t, []string{
		f.claude,
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
		"--append-system-prompt-file", "/etc/aibox/task.md",
		"--model", "opus",
		"--max-turns", "3",
		"--max-budget-usd", "1.5",
	}, calls[0].Args)
	assert.Equal(t, "Fix the README.\n{\"type\":\"result\"}\n", string(r.files[task.TranscriptFile]))
	assert.Equal(t, "warning\n", string(r.files[task.LogFile]))
	assert.Empty(t, r.result.Truncated)
}

func TestTaskLeavesOutTheSettingsThatAreNotSet(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "")

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)

	calls := f.processes.claudeCalls(f.claude)
	require.Len(t, calls, 1)
	assert.NotContains(t, calls[0].Args, "--model")
	assert.NotContains(t, calls[0].Args, "--max-turns")
	assert.NotContains(t, calls[0].Args, "--max-budget-usd")
}

func TestTaskReportsTheExitCodeOfClaudeCode(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "exit 3\n")

	// act
	code, r := f.run()

	// assert
	assert.Equal(t, 0, code, "a failure of Claude Code is part of the result")
	require.NotNil(t, r.result.ClaudeExitCode)
	assert.Equal(t, 3, *r.result.ClaudeExitCode)
}

func TestTaskWarnsWhenItCannotCommitTheLeftoversAndSendsTheCommits(t *testing.T) {
	// arrange
	f := newTaskFixture(t, `
git -c user.name=Claude -c user.email=claude@example.com commit --quiet --allow-empty --message "committed"
echo left > left
touch .git/index.lock
`)

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	assert.False(t, r.result.Leftovers)
	require.Len(t, r.result.Warnings, 1)
	assert.Contains(t, r.result.Warnings[0], "commit what the task left uncommitted")
	assert.Equal(t, "committed by Claude\n", f.fetch(r))
}

func TestTaskEndsWhatEachStepLeftRunning(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "echo left > left\n")

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)

	var want []string
	for range f.processes.started {
		want = append(want, "/sys/fs/cgroup/user", "/sys/fs/cgroup/aibox")
	}

	assert.Equal(t, want, f.processes.kills)
}

func TestTaskDoesNotWaitForWhatClaudeCodeLeftHoldingItsOutput(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "sleep 100 &\necho done\n")

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, "done\n", string(r.files[task.TranscriptFile]))
}

func TestTaskRunsEachStepInANewCgroup(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "echo left > left\n")

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)

	var cgroups []string

	for _, s := range f.processes.startedCopy() {
		parent := "/sys/fs/cgroup/aibox/"
		if s.cmd.Path == f.claude {
			parent = "/sys/fs/cgroup/user/"
			assert.Contains(t, f.processes.delegated, s.cgroup, "the user may make cgroups below the one of Claude Code")
		}

		assert.True(t, strings.HasPrefix(s.cgroup, parent), "%s runs in %s", s.cmd.Args, s.cgroup)
		assert.Contains(t, f.processes.made, s.cgroup)
		assert.NotContains(t, cgroups, s.cgroup, "a killed cgroup kills what starts in it")
		cgroups = append(cgroups, s.cgroup)
	}
}

func TestTaskStopsWhenWhatWasLeftRunningCannotBeEnded(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "")
	f.processes.failKill = errors.New("still populated")

	// act
	code, r := f.run()

	// assert
	assert.Equal(t, 1, code)
	assert.Equal(t, []string{"result.json"}, r.names)
	assert.Contains(t, r.result.Error, "could not be ended")
	assert.Contains(t, r.result.Error, "still populated")
	assert.Nil(t, r.result.ClaudeExitCode)
}

func TestTaskSendsTheTranscriptWhenWhatClaudeCodeLeftCannotBeEnded(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "echo '{\"type\":\"result\"}'\nexit 2\n")
	f.processes.failKillAfter = f.claude

	// act
	code, r := f.run()

	// assert
	assert.Equal(t, 1, code)
	assert.Equal(t, []string{"transcript.jsonl", "claude.log", "result.json"}, r.names)
	assert.Equal(t, "{\"type\":\"result\"}\n", string(r.files[task.TranscriptFile]))
	require.NotNil(t, r.result.ClaudeExitCode)
	assert.Equal(t, 2, *r.result.ClaudeExitCode)
	assert.Contains(t, r.result.Error, "run Claude Code: what the step left running could not be ended")
}

func TestTaskStopsClaudeCodeAfterTheTimeoutAndSendsItsWork(t *testing.T) {
	// arrange
	f := newTaskFixture(t, `
git commit --quiet --allow-empty --message "before the timeout"
sleep 100
`)
	f.writeInput(task.SettingsFile, `{"timeoutSeconds":2}`)

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	assert.True(t, r.result.TimedOut)
	assert.Equal(t, "before the timeout by aibox\n", f.fetch(r))
	assert.Contains(t, f.progress.String(), "Claude Code ran out of time")
}

func TestTaskCommitsUnderTheGitIdentityOfTheSettings(t *testing.T) {
	// arrange
	f := newTaskFixture(t, `git commit --quiet --allow-empty --message "mine"`+"\n")
	f.writeInput(task.SettingsFile, `{"gitName":"Some One","gitEmail":"someone@example.com"}`)

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, "mine by Some One\n", f.fetch(r))
}

func TestTaskLeavesTheTaskNoRemote(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "git remote > remotes\n")

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	f.fetch(r)
	assert.Empty(t, f.gitIn(f.source, "show", "changes:remotes"))
}

func TestTaskSendsTheLastLineOfClaudeCodeAsItsResult(t *testing.T) {
	tests := map[string]string{
		"echo '{\"type\":\"system\"}'\necho '{\"type\":\"result\",\"total_cost_usd\":0.5}'\n": `{"type":"result","total_cost_usd":0.5}`,
		"printf '{\"type\":\"result\"}'\n":                                           `{"type":"result"}`,
		"echo '{\"type\":\"result\"}'\necho 'Not logged in'\n":                       "",
		"echo '{\"type\":\"result\"}'\necho\necho '  '\n":                            `{"type":"result"}`,
		"echo '{\"type\":\"result\"}'\necho null\n":                                  "",
		"echo '{\"type\":\"result\"}'\nhead -c 70000 /dev/zero | tr '\\0' a\n":       "",
		"echo '{\"type\":\"result\"}'\nhead -c 70000 /dev/zero | tr '\\0' a\necho\n": "",
		"": "",
	}

	for script, want := range tests {
		t.Run(script, func(t *testing.T) {
			// arrange
			f := newTaskFixture(t, script)

			// act
			code, r := f.run()

			// assert
			require.Equal(t, 0, code, r.result.Error)
			assert.Equal(t, want, string(r.result.ClaudeResult))
		})
	}
}

// evilFilter makes git add run a clean filter of the task on the file
// left, with the script as its body.
const evilFilter = `
printf '#!/bin/sh\n%s\n' "$FILTER" > .git/filter
chmod +x .git/filter
git config filter.evil.clean "$PWD/.git/filter"
git config filter.evil.required true
echo 'left filter=evil' > .gitattributes
echo left > left
`

func TestTaskShowsWhatGitPrintedOnOneLineWithoutControlCharacters(t *testing.T) {
	// arrange
	f := newTaskFixture(t, `FILTER='printf "\033]52;c;ZXZpbA==\007done\naibox: fake\n" >&2; exit 1'`+evilFilter)

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	require.Len(t, r.result.Warnings, 1)
	assert.Contains(t, r.result.Warnings[0], "?]52;c;ZXZpbA==?done | aibox: fake")
	assert.NotContains(t, f.progress.String(), "\x1b")
	assert.NotContains(t, f.progress.String(), "\naibox: fake")
}

func TestTaskStopsTheGitStepsAfterTheirTime(t *testing.T) {
	// arrange
	// the steps before Claude Code have the same time, which they need far
	// less of
	f := newTaskFixture(t, `FILTER='sleep 100'`+evilFilter)
	f.gitTimeout = 3 * time.Second

	// act
	code, r := f.run()

	// assert
	assert.Equal(t, 1, code)
	require.Len(t, r.result.Warnings, 1)
	assert.Contains(t, r.result.Warnings[0], "the git steps took too long")
	assert.Contains(t, r.result.Error, "the git steps took too long")
	assert.Equal(t, []string{"transcript.jsonl", "claude.log", "result.json"}, r.names)
}

func TestTaskRunsNoHooksOfTheTask(t *testing.T) {
	// arrange
	f := newTaskFixture(t, `
printf '#!/bin/sh\nexit 1\n' > .git/hooks/prepare-commit-msg
chmod +x .git/hooks/prepare-commit-msg
git config core.fsmonitor "$PWD/.git/hooks/prepare-commit-msg"
echo left > left
`)

	// act
	code, r := f.run()

	// assert
	require.Equal(t, 0, code, r.result.Error)
	assert.Empty(t, r.result.Warnings)
	assert.True(t, r.result.Leftovers)
}

func TestTaskFailsWithBadSettings(t *testing.T) {
	for _, settings := range []string{
		`{"maxTurns":-1}`,
		`{"maxBudgetUSD":-1}`,
		`{"model":"--dangerously-skip-permissions"}`,
		`{"model":"opus sonnet"}`,
		`{"unknown":1}`,
		`{"timeoutSeconds":-1}`,
		`{"gitName":"a\nb"}`,
		`{"gitEmail":"a b@example.com"}`,
		`{"gitName":"a <b@example.com>"}`,
		`{} {}`,
		`not json`,
	} {
		t.Run(settings, func(t *testing.T) {
			// arrange
			f := newTaskFixture(t, "")
			f.writeInput(task.SettingsFile, settings)

			// act
			code, r := f.run()

			// assert
			assert.Equal(t, 1, code)
			assert.Equal(t, []string{"result.json"}, r.names)
			assert.Contains(t, r.result.Error, task.ErrBadSettings.Error())
			assert.Empty(t, f.processes.started)
		})
	}
}

func TestTaskFailsWhenTheInputCannotBeCloned(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "")
	require.NoError(t, os.Remove(filepath.Join(f.input, task.InputBundle)))

	// act
	code, r := f.run()

	// assert
	assert.Equal(t, 1, code)
	assert.Contains(t, r.result.Error, "clone the input: git exited with")
	assert.Empty(t, f.processes.claudeCalls(f.claude))
}

func TestTaskFailsWhenThePromptIsMissing(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "")
	require.NoError(t, os.Remove(filepath.Join(f.input, task.PromptFile)))

	// act
	code, r := f.run()

	// assert
	assert.Equal(t, 1, code)
	assert.Contains(t, r.result.Error, "open the prompt")
	assert.Empty(t, f.processes.claudeCalls(f.claude))
}

func TestTaskSendsItsResultsAndWhatItDoesOverASession(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "echo fixed > README\n")
	guestSide, hostSide := tcpPair(t)

	served := make(chan error, 1)

	go func() {
		served <- session.Serve(guestSide, func(session.Request) (session.Process, error) {
			return guest.StartTask(f.processes, f.command, f.input, f.out), nil
		})
	}()

	var out, progress bytes.Buffer

	client := session.Exec{In: strings.NewReader(""), Out: &out, Errors: &progress}

	// act
	code, err := client.Run(hostSide)

	// assert
	require.NoError(t, err)
	assert.Equal(t, 0, code)

	r := readResults(t, &out)
	assert.Equal(t, []string{"transcript.jsonl", "claude.log", "changes.bundle", "result.json"}, r.names)
	assert.Contains(t, progress.String(), "cloning the input\n")
	assert.Contains(t, progress.String(), "bundling the changes\n")
	require.NoError(t, <-served)
}

// tcpPair is a connection over the loopback. net.Pipe does not do, since
// both ends of SSH write before they read.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = listener.Close() }()

	dialed, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)

	accepted, err := listener.Accept()
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = dialed.Close()
		_ = accepted.Close()
	})

	return accepted, dialed
}
