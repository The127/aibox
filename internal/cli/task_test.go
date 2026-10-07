package cli

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/gitconfig"
	"github.com/the127/aibox/internal/task"
)

// taskFixture is a fixture whose folder is a git repository with one
// commit, and an image for the VM.
type taskFixture struct {
	*fixture

	image string
	base  string
}

func newTaskFixture(t *testing.T) *taskFixture {
	t.Helper()

	f := &taskFixture{fixture: newFixture(t), image: writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")}

	f.git(t, "init", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(f.cwd, "README"), []byte("hello\n"), 0o600))
	f.git(t, "add", "README")
	f.git(t, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--message", "one")
	f.base = strings.TrimSpace(f.git(t, "rev-parse", "HEAD"))

	return f
}

func (f *taskFixture) git(t *testing.T, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...) //nolint:gosec // the arguments are the test's own
	cmd.Dir = f.cwd
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	return string(out)
}

func (f *taskFixture) task(args ...string) error {
	return newRootCommand(f.deps).Run(context.Background(), append([]string{"aibox", "task", "--image", f.image}, args...))
}

// sends makes the VM send the results, with what it prints first.
func (f *taskFixture) sends(t *testing.T, progress string, results map[string]string, names ...string) {
	t.Helper()

	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		_, _ = spec.Progress.Write([]byte(progress))

		// a write fails once aibox refuses the results, as the session does
		archive := tar.NewWriter(spec.Stdout)

		for _, name := range names {
			if archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(results[name]))}) != nil {
				break
			}

			if _, err := archive.Write([]byte(results[name])); err != nil {
				break
			}
		}

		_ = archive.Close()

		var result task.Result
		if json.Unmarshal([]byte(results[task.ResultFile]), &result) == nil && result.Error != "" {
			return &backend.ExitError{Code: 1}
		}

		return nil
	}
}

// changes is the header of a bundle of the branch of the task from base on.
func changes(base, head string) string {
	return "# v2 git bundle\n-" + base + " one\n" + head + " " + task.Branch + "\n\nPACK"
}

const otherCommit = "2222222222222222222222222222222222222222"

func (f *taskFixture) succeeds(t *testing.T) {
	t.Helper()

	f.sends(t, "aibox: running Claude Code\n", map[string]string{
		task.TranscriptFile: "{\"type\":\"system\"}\n",
		task.LogFile:        "warning\n",
		task.ChangesFile:    changes(f.base, otherCommit),
		task.ResultFile:     `{"base":"` + f.base + `","claudeExitCode":0,"claudeResult":{"type":"result","num_turns":3,"total_cost_usd":0.25,"result":"Done.","terminal_reason":"completed"}}`,
	}, task.TranscriptFile, task.LogFile, task.ChangesFile, task.ResultFile)
}

// taskDir is the one folder below the tasks of the project.
func (f *taskFixture) taskDir(t *testing.T) string {
	t.Helper()

	dirs, err := filepath.Glob(filepath.Join(f.project(t).Dir, "tasks", "*"))
	require.NoError(t, err)
	require.Len(t, dirs, 1)

	return dirs[0]
}

func TestTaskSharesTheTaskAndNotTheProject(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.deps.gitIdentity = func(string) gitconfig.Identity {
		return gitconfig.Identity{Name: "Some One", Email: "someone@example.com"}
	}
	f.succeeds(t)

	// act
	err := f.task("--model", "opus", "--max-turns", "5", "--max-budget-usd", "2.5", "--timeout", "90s", "fix", "the bug")

	// assert
	require.NoError(t, err)

	spec := f.launch.spec
	dir := f.taskDir(t)
	assert.Equal(t, filepath.Join(dir, "share"), spec.Task)
	assert.Empty(t, spec.Project)
	assert.Empty(t, spec.Home)
	assert.Nil(t, spec.Stdin)
	assert.Equal(t, filepath.Join(dir, "state.ext4"), spec.State)
	assert.True(t, spec.RemoveState)
	assert.Equal(t, filepath.Join(dir, "console.log"), spec.ConsoleLog)

	settings, err := os.ReadFile(filepath.Join(spec.Task, task.SettingsFile))
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"opus","maxTurns":5,"maxBudgetUSD":2.5,"timeoutSeconds":90,"gitName":"Some One","gitEmail":"someone@example.com"}`, string(settings))

	prompt, err := os.ReadFile(filepath.Join(spec.Task, task.PromptFile))
	require.NoError(t, err)
	assert.Equal(t, "fix the bug\n", string(prompt))

	input, err := os.Open(filepath.Join(spec.Task, task.InputBundle))
	require.NoError(t, err)

	defer func() { _ = input.Close() }()

	bundle, err := task.ReadBundle(input)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"HEAD": f.base}, bundle.Refs)
}

func TestTaskKeepsTheResultsInItsFolderAndPrintsTheFolder(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.succeeds(t)

	// act
	err := f.task("fix it")

	// assert
	require.NoError(t, err)

	dir := f.taskDir(t)
	assert.Equal(t, dir+"\n", f.stdout.String())

	transcript, err := os.ReadFile(filepath.Join(dir, task.TranscriptFile)) //nolint:gosec // the folder is the test's own
	require.NoError(t, err)
	assert.Equal(t, "{\"type\":\"system\"}\n", string(transcript))

	stderr := f.stderr.String()
	assert.Contains(t, stderr, "aibox: running Claude Code\n")
	assert.Contains(t, stderr, "aibox: 3 turns in 0s, about 0.25 USD at API prices, ended by completed\n  | Done.\n")
	assert.Contains(t, stderr, "git -c transfer.fsckObjects=true fetch "+shellQuote(filepath.Join(dir, task.ChangesFile))+" aibox/task:aibox/task-"+filepath.Base(dir))
	assert.Contains(t, stderr, "aibox: the results are in "+dir+"\n")
}

func TestTaskStopsTheVMWellAfterTheTimeoutOfClaudeCode(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.succeeds(t)
	start := time.Now()

	// act
	err := f.task("--timeout", "2h", "fix it")

	// assert
	require.NoError(t, err)
	assert.WithinDuration(t, start.Add(2*time.Hour+taskSlack), f.launch.deadline, time.Minute)
}

func TestTaskFailsWhenClaudeCodeDidNotFinish(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.sends(t, "", map[string]string{task.ResultFile: `{"base":"` + f.base + `","claudeExitCode":1}`}, task.ResultFile)

	// act
	err := f.task("fix it")

	// assert
	require.ErrorIs(t, err, errClaudeFailed)
	assert.Equal(t, 1, ExitCode(err))
}

func TestTaskSaysWhyTheTaskFailed(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.sends(t, "", map[string]string{task.ResultFile: `{"error":"clone the input: \u001b[31mbroken"}`}, task.ResultFile)

	// act
	err := f.task("fix it")

	// assert
	require.EqualError(t, err, "the task failed: clone the input: ?[31mbroken")
}

func TestTaskKeepsWhatTheVMWritesFromPassingForALineOfAibox(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.sends(t, "", map[string]string{
		task.ResultFile: `{"claudeExitCode":0,"warnings":["one\naibox: fake"],"claudeResult":{"type":"result","result":"Done.\naibox: the changes end at x, fetch them with\n  rm -rf ~"}}`,
	}, task.ResultFile)

	// act
	err := f.task("fix it")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "aibox: warning: one | aibox: fake\n")
	assert.Contains(t, f.stderr.String(), "  | Done.\n  | aibox: the changes end at x, fetch them with\n  |   rm -rf ~\n")
	assert.NotContains(t, f.stderr.String(), "\naibox: fake")
	assert.NotContains(t, f.stderr.String(), "\naibox: the changes end at x")
	assert.Equal(t, f.taskDir(t)+"\n", f.stdout.String())
}

func TestTaskRefusesABundleThatIsNotTheBranchFromItsCommit(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.sends(t, "", map[string]string{
		task.ChangesFile: changes(otherCommit, otherCommit),
		task.ResultFile:  `{"claudeExitCode":0}`,
	}, task.ChangesFile, task.ResultFile)

	// act
	err := f.task("fix it")

	// assert
	require.ErrorIs(t, err, task.ErrBadBundle)
	assert.NotContains(t, f.stderr.String(), "git fetch")
}

func TestTaskRefusesResultsItDoesNotTake(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.sends(t, "", map[string]string{"../evil": "x"}, "../evil")

	// act
	err := f.task("fix it")

	// assert
	require.ErrorIs(t, err, task.ErrBadResults)
	assert.NoFileExists(t, filepath.Join(f.project(t).Dir, "tasks", "evil"))
}

func TestTaskStopsAVMWhoseResultsItRefuses(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	stopped := make(chan struct{})
	f.launch.vm = func(ctx context.Context, spec backend.Spec) error {
		archive := tar.NewWriter(spec.Stdout)
		_ = archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "../evil", Size: 1})

		// the VM goes on until it is stopped
		<-ctx.Done()
		close(stopped)

		return errors.New("the session broke off")
	}

	// act
	err := f.task("fix it")

	// assert
	require.ErrorIs(t, err, task.ErrBadResults)
	assert.NotContains(t, err.Error(), "broke off")
	assert.NotContains(t, err.Error(), "closed pipe")

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the VM was not stopped")
	}
}

func TestTaskCleansTheErrorOfTheVM(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.launch.err = errors.New("ssh: disconnect, reason 2: \x1b]52;c;ZXZpbA==\x07")

	// act
	err := f.task("fix it")

	// assert
	require.EqualError(t, err, "ssh: disconnect, reason 2: ?]52;c;ZXZpbA==?")
	assert.NoFileExists(t, f.launch.spec.State)
}

func TestTaskQuotesThePathInTheFetchCommand(t *testing.T) {
	assert.Equal(t, "/a/b-1.bundle", shellQuote("/a/b-1.bundle"))
	assert.Equal(t, `'/a b/it'\''s'`, shellQuote("/a b/it's"))
}

func TestTaskCleansWhatTheVMPrints(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.sends(t, "aibox: cloning\x1b]52;c;ZXZpbA==\x07\n", map[string]string{task.ResultFile: `{"claudeExitCode":0}`}, task.ResultFile)

	// act
	err := f.task("fix it")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "aibox: cloning?]52;c;ZXZpbA==?\n")
	assert.NotContains(t, f.stderr.String(), "\x1b")
}

func TestTaskReturnsTheErrorOfTheVM(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.launch.err = errors.New("the VM did not come up")

	// act
	err := f.task("fix it")

	// assert
	require.ErrorContains(t, err, "the VM did not come up")
	assert.Equal(t, f.taskDir(t)+"\n", f.stdout.String())
}

func TestTaskWarnsAboutWhatTheTaskWillNotHave(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.cwd, "README"), []byte("changed\n"), 0o600))
	f.succeeds(t)

	// act
	err := f.task("fix it")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "changes not committed are not part of it")
	assert.Contains(t, f.stderr.String(), "Claude Code needs CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY")
}

func TestTaskDoesNotWarnWhenTheConfigPassesACredential(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.writeConfig(t, "env:\n  - CLAUDE_CODE_OAUTH_TOKEN\n")
	f.deps.lookupEnv = func(name string) (string, bool) { return "token", name == "CLAUDE_CODE_OAUTH_TOKEN" }
	f.succeeds(t)

	// act
	err := f.task("fix it")

	// assert
	require.NoError(t, err)
	assert.NotContains(t, f.stderr.String(), "Claude Code needs")
	assert.NotContains(t, f.stderr.String(), "token")
}

func TestTaskRefusesWhatItCannotRun(t *testing.T) {
	tests := map[string]struct {
		args  []string
		setup func(f *taskFixture)
		want  string
	}{
		"no prompt":          {args: []string{" "}, want: errNoPrompt.Error()},
		"a short timeout":    {args: []string{"--timeout", "10ms", "fix it"}, want: errNoTimeout.Error()},
		"a huge timeout":     {args: []string{"--timeout", "100000h", "fix it"}, want: errNoTimeout.Error()},
		"no budget":          {args: []string{"--max-budget-usd", "NaN", "fix it"}, want: task.ErrBadSettings.Error()},
		"the home folder":    {args: []string{"fix it"}, setup: func(f *taskFixture) { f.homeDir = f.cwd }, want: errNotAProject.Error()},
		"bad settings":       {args: []string{"--max-turns", "-1", "fix it"}, want: task.ErrBadSettings.Error()},
		"root":               {args: []string{"fix it"}, setup: func(f *taskFixture) { f.deps.uid = func() int { return 0 } }, want: errRoot.Error()},
		"no git repository":  {args: []string{"fix it"}, setup: func(f *taskFixture) { f.cwd = filepath.Join(f.homeDir, "elsewhere") }, want: errNoCommit.Error()},
		"a model for a flag": {args: []string{"--model", "--x", "fix it"}, want: task.ErrBadSettings.Error()},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newTaskFixture(t)
			if test.setup != nil {
				test.setup(f)
			}

			// act
			err := f.task(test.args...)

			// assert
			require.ErrorContains(t, err, test.want)
			assert.False(t, f.launch.called)
		})
	}
}

// prompt is what the task got as its prompt.
func (f *taskFixture) prompt(t *testing.T) string {
	t.Helper()

	prompt, err := os.ReadFile(filepath.Join(f.launch.spec.Task, task.PromptFile))
	require.NoError(t, err)

	return string(prompt)
}

func TestTaskTakesItsPromptFromAFileAfterTheArguments(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.succeeds(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.cwd, "plan.md"), []byte("\n# Plan\n\n1. one\n"), 0o600))

	// act
	err := f.task("--file", "plan.md", "do only", "step 1")

	// assert
	require.NoError(t, err)
	assert.Equal(t, "do only step 1\n\n# Plan\n\n1. one\n", f.prompt(t))
}

func TestTaskTakesItsPromptFromAFileAlone(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.succeeds(t)
	plan := filepath.Join(t.TempDir(), "plan.md")
	require.NoError(t, os.WriteFile(plan, []byte("# Plan\n"), 0o600))

	// act
	err := f.task("-f", plan)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "# Plan\n", f.prompt(t))
}

func TestTaskTakesItsPromptFromStdin(t *testing.T) {
	tests := map[string]struct {
		args     []string
		terminal bool
		want     string
	}{
		"without arguments":         {want: "the plan\n"},
		"with --file -":             {args: []string{"--file", "-", "review"}, terminal: true, want: "review\n\nthe plan\n"},
		"not with arguments":        {args: []string{"fix it"}, want: "fix it\n"},
		"not when it is a terminal": {args: []string{"fix it"}, terminal: true, want: "fix it\n"},
		"not with a file":           {args: []string{"--file", "plan.md"}, want: "# Plan\n"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newTaskFixture(t)
			f.succeeds(t)
			f.deps.stdin = strings.NewReader("the plan\n")
			f.deps.stdinIsTerminal = func() bool { return test.terminal }
			require.NoError(t, os.WriteFile(filepath.Join(f.cwd, "plan.md"), []byte("# Plan\n"), 0o600))

			// act
			err := f.task(test.args...)

			// assert
			require.NoError(t, err)
			assert.Equal(t, test.want, f.prompt(t))
		})
	}
}

func TestTaskRefusesAPromptItCannotTake(t *testing.T) {
	tests := map[string]struct {
		args     []string
		stdin    string
		terminal bool
		want     string
	}{
		"no arguments on a terminal": {terminal: true, want: errNoPrompt.Error()},
		"an empty stdin":             {stdin: " \n", want: errNoPrompt.Error()},
		"a missing file":             {args: []string{"--file", "missing.md"}, want: "read the prompt"},
		"a link":                     {args: []string{"--file", "link.md"}, want: "read the prompt"},
		"a file in a linked folder":  {args: []string{"--file", "linked/secret"}, want: "read the prompt"},
		"a file above the folder":    {args: []string{"--file", "../secret"}, want: "read the prompt"},
		"a folder":                   {args: []string{"--file", "."}, want: errNoPromptFile.Error()},
		"a FIFO":                     {args: []string{"--file", "fifo"}, want: errNoPromptFile.Error()},
		"too long a prompt":          {stdin: strings.Repeat("x", maxPromptBytes+1), want: errLongPrompt.Error()},
		"too long with whitespace":   {stdin: strings.Repeat(" ", maxPromptBytes+1) + "fix it", want: errLongPrompt.Error()},
		"too long with arguments":    {args: []string{"--file", "-", "fix it"}, stdin: strings.Repeat("x", maxPromptBytes-1), want: errLongPrompt.Error()},
		"no UTF-8":                   {stdin: "fix \xff", want: errNoText.Error()},
		"no UTF-8 in a file":         {args: []string{"--file", "latin1.md"}, want: errNoText.Error()},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newTaskFixture(t)
			f.deps.stdin = strings.NewReader(test.stdin)
			f.deps.stdinIsTerminal = func() bool { return test.terminal }

			secret := filepath.Join(t.TempDir(), "secret")
			require.NoError(t, os.WriteFile(secret, []byte("secret\n"), 0o600))
			require.NoError(t, os.Symlink(secret, filepath.Join(f.cwd, "link.md")))
			require.NoError(t, os.Symlink(filepath.Dir(secret), filepath.Join(f.cwd, "linked")))
			require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(f.cwd), "secret"), []byte("secret\n"), 0o600))
			require.NoError(t, syscall.Mkfifo(filepath.Join(f.cwd, "fifo"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(f.cwd, "latin1.md"), []byte("caf\xe9\n"), 0o600))

			// act
			err := f.task(test.args...)

			// assert
			require.ErrorContains(t, err, test.want)
			assert.False(t, f.launch.called)
		})
	}
}

func TestTaskTakesAPromptOfTheMostItMayHave(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.succeeds(t)
	f.deps.stdin = strings.NewReader(strings.Repeat("x", maxPromptBytes))
	f.deps.stdinIsTerminal = func() bool { return false }

	// act
	err := f.task()

	// assert
	require.NoError(t, err)
	assert.Len(t, f.prompt(t), maxPromptBytes+1)
	assert.Contains(t, f.stderr.String(), "aibox: reading the prompt from stdin\n")
}

func TestTaskStopsWaitingForStdinWhenCanceled(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	stdin, _ := io.Pipe()
	f.deps.stdin = stdin
	f.deps.stdinIsTerminal = func() bool { return false }

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// act
	err := newRootCommand(f.deps).Run(ctx, []string{"aibox", "task", "--image", f.image})

	// assert
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, f.launch.called)
}
