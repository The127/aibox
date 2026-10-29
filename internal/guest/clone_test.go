//go:build linux

package guest_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/guest"
	"github.com/the127/aibox/internal/task"
	"github.com/the127/aibox/internal/vm"
)

// newCloneFixture is a task fixture whose share holds clone.json with the
// base of the input bundle and the remotes.
func newCloneFixture(t *testing.T, remotes ...task.Remote) *taskFixture {
	t.Helper()

	f := newTaskFixture(t, "")
	f.writeClone(task.Clone{Base: f.base, Remotes: remotes})

	return f
}

func (f *taskFixture) writeClone(clone task.Clone) {
	content, err := json.Marshal(clone)
	require.NoError(f.t, err)
	f.writeInput(task.CloneFile, string(content))
}

func (f *taskFixture) prepare(mode string) error {
	return guest.PrepareClone(f.processes, f.command, f.input, mode)
}

func (f *taskFixture) collect() (int, results) {
	var out bytes.Buffer

	code := guest.RunCollect(f.processes, f.command, f.input, f.out, &out, &f.progress)

	return code, readResults(f.t, &out)
}

// fetchClone is fetch for the branch of a clone.
func (f *taskFixture) fetchClone(r results) string {
	bundle := filepath.Join(f.t.TempDir(), task.ChangesFile)
	require.NoError(f.t, os.WriteFile(bundle, r.files[task.ChangesFile], 0o600))
	f.gitIn(f.source, "fetch", "--quiet", bundle, task.CloneBranch+":refs/heads/changes")

	return f.gitIn(f.source, "log", "--format=%s by %an", f.base+"..changes")
}

// fetchEnd is fetch for the last commit of a session that ended apart from
// the branch of the clone.
func (f *taskFixture) fetchEnd(r results) string {
	bundle := filepath.Join(f.t.TempDir(), task.ChangesFile)
	require.NoError(f.t, os.WriteFile(bundle, r.files[task.ChangesFile], 0o600))
	f.gitIn(f.source, "fetch", "--quiet", bundle, task.CloneEndBranch+":refs/heads/end")

	return f.gitIn(f.source, "log", "--format=%s by %an", f.base+"..end")
}

// bundleRefs are the names of the refs the bundle of the changes carries,
// one per line.
func (f *taskFixture) bundleRefs(r results) string {
	bundle := filepath.Join(f.t.TempDir(), task.ChangesFile)
	require.NoError(f.t, os.WriteFile(bundle, r.files[task.ChangesFile], 0o600))

	var names []string
	for line := range strings.SplitSeq(strings.TrimSpace(f.gitIn(f.source, "bundle", "list-heads", bundle)), "\n") {
		_, name, _ := strings.Cut(line, " ")
		names = append(names, name)
	}

	return strings.Join(names, "\n") + "\n"
}

func TestANewCloneIsOnItsBranchWithTheRemotesOfTheHost(t *testing.T) {
	// arrange
	f := newCloneFixture(t, task.Remote{Name: "origin", URL: "https://github.com/owner/repo.git"}, task.Remote{Name: "fork", URL: "git@github.com:me/repo.git"})

	// act
	err := f.prepare(vm.CloneNew)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "aibox/clone\n", f.gitIn(f.project, "branch", "--show-current"))
	assert.Equal(t, f.base+"\n", f.gitIn(f.project, "rev-parse", "HEAD"))
	assert.Equal(t, "fork\tgit@github.com:me/repo.git (fetch)\nfork\tgit@github.com:me/repo.git (push)\norigin\thttps://github.com/owner/repo.git (fetch)\norigin\thttps://github.com/owner/repo.git (push)\n", f.gitIn(f.project, "remote", "--verbose"))
	assert.FileExists(t, filepath.Join(f.project, "README"))
	assert.Equal(t, "committed,index\n", f.gitIn(f.project, "config", "core.fsync"), "a commit survives a VM that ends without flushing its disk")
}

func TestANewCloneRunsNoFilterOfTheGitConfigOfTheHome(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	require.NoError(t, os.WriteFile(filepath.Join(f.home, "attributes"), []byte("* filter=planted\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(f.home, ".gitconfig"), []byte("[core]\n\tattributesFile = "+filepath.Join(f.home, "attributes")+"\n[filter \"planted\"]\n\tsmudge = touch "+marker+"; cat\n"), 0o600))

	// act
	err := f.prepare(vm.CloneNew)

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, marker)
}

func TestANewCloneFailsWhenTheInputIsNotAtTheBase(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "")
	f.writeClone(task.Clone{Base: strings.Repeat("a", 40)})

	// act
	err := f.prepare(vm.CloneNew)

	// assert
	assert.ErrorContains(t, err, "not at the base")
}

func TestANewCloneFailsWithBadSettings(t *testing.T) {
	// arrange
	f := newTaskFixture(t, "")
	f.writeInput(task.CloneFile, `{"base":"`+f.base+`","prompt":"x"}`)

	// act
	err := f.prepare(vm.CloneNew)

	// assert
	assert.ErrorIs(t, err, task.ErrBadSettings)
}

func TestAKeptCloneNeedsAClone(t *testing.T) {
	// arrange
	f := newCloneFixture(t)

	// act
	err := f.prepare(vm.CloneKeep)

	// assert
	assert.ErrorContains(t, err, "no clone to go on with")
}

func TestAKeptCloneMustHaveStartedFromTheBase(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	require.NoError(t, f.prepare(vm.CloneNew))
	f.writeClone(task.Clone{Base: strings.Repeat("a", 40)})

	// act
	err := f.prepare(vm.CloneKeep)

	// assert
	assert.ErrorContains(t, err, "did not start from")
}

func TestANewCloneEndsOnlyTheCgroupsOfItsSteps(t *testing.T) {
	// arrange
	f := newCloneFixture(t)

	// act
	err := f.prepare(vm.CloneNew)

	// assert
	require.NoError(t, err)
	assert.NotEmpty(t, f.processes.kills)
	assert.NotContains(t, f.processes.kills, "/sys/fs/cgroup/user", "the session starts in a cgroup of the user afterwards")
	assert.Contains(t, f.processes.made, "/sys/fs/cgroup/aibox/clone1")
	assert.Empty(t, f.processes.delegated)
}

func TestAKeptCloneLeavesTheCloneAsItIs(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	require.NoError(t, f.prepare(vm.CloneNew))
	require.NoError(t, os.WriteFile(filepath.Join(f.project, "README"), []byte("changed\n"), 0o600))

	// act
	err := f.prepare(vm.CloneKeep)

	// assert
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(f.project, "README"))
	require.NoError(t, err)
	assert.Equal(t, "changed\n", string(content))
}

func TestCollectSendsTheCommitsAndWhatWasLeftUncommitted(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	require.NoError(t, f.prepare(vm.CloneNew))
	require.NoError(t, os.WriteFile(filepath.Join(f.project, "README"), []byte("fixed\n"), 0o600))
	f.gitIn(f.project, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--all", "--message", "fix the README")
	require.NoError(t, os.WriteFile(filepath.Join(f.project, "NOTES"), []byte("todo\n"), 0o600))
	f.processes.kills = nil

	// act
	code, r := f.collect()

	// assert
	assert.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, []string{task.ChangesFile, task.ResultFile}, r.names)
	assert.Equal(t, f.base, r.result.Base)
	assert.True(t, r.result.Leftovers)
	assert.Equal(t, "aibox: what the session left uncommitted by aibox\nfix the README by Someone\n", f.fetchClone(r))
	assert.Equal(t, "/sys/fs/cgroup/user", f.processes.kills[0], "what the session left running is ended first")
	assert.NotContains(t, f.processes.kills[1:], "/sys/fs/cgroup/user", "the steps end only their own cgroups")
}

func TestCollectStillSendsTheCommitsWhenWhatRunsCannotBeEnded(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	require.NoError(t, f.prepare(vm.CloneNew))
	f.gitIn(f.project, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--allow-empty", "--message", "work")
	f.processes.failKillOf = "/sys/fs/cgroup/user"

	// act
	code, r := f.collect()

	// assert
	assert.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, []string{"end what the session left running: still populated"}, r.result.Warnings)
	assert.Equal(t, "work by Someone\n", f.fetchClone(r))
}

func TestCollectSendsTheLastCommitOfAnotherBranch(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	require.NoError(t, f.prepare(vm.CloneNew))
	f.gitIn(f.project, "switch", "--quiet", "--create", "elsewhere")
	f.gitIn(f.project, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--allow-empty", "--message", "on another branch")

	// act
	code, r := f.collect()

	// assert
	assert.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, "on another branch by Someone\n", f.fetchClone(r))
}

func TestCollectKeepsTheBranchOfTheCloneWhenTheSessionEndsBehindIt(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	require.NoError(t, f.prepare(vm.CloneNew))
	f.gitIn(f.project, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--allow-empty", "--message", "work")
	f.gitIn(f.project, "switch", "--quiet", "--detach", f.base)

	// act
	code, r := f.collect()

	// assert
	assert.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, "work by Someone\n", f.fetchClone(r))
	assert.Equal(t, task.CloneBranch+"\n", f.bundleRefs(r))
}

func TestCollectSendsBothBranchesWhenTheSessionEndsOnABranchApart(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	require.NoError(t, f.prepare(vm.CloneNew))
	f.gitIn(f.project, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--allow-empty", "--message", "on the clone")
	f.gitIn(f.project, "switch", "--quiet", "--create", "apart", f.base)
	f.gitIn(f.project, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--allow-empty", "--message", "apart")

	// act
	code, r := f.collect()

	// assert
	assert.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, task.CloneBranch+"\n"+task.CloneEndBranch+"\n", f.bundleRefs(r))
	assert.Equal(t, "on the clone by Someone\n", f.fetchClone(r))
	assert.Equal(t, "apart by Someone\n", f.fetchEnd(r))
}

func TestCollectSendsWhatWasLeftBehindTheBranchOfTheClone(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	require.NoError(t, f.prepare(vm.CloneNew))
	f.gitIn(f.project, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--allow-empty", "--message", "work")
	f.gitIn(f.project, "switch", "--quiet", "--detach", f.base)
	require.NoError(t, os.WriteFile(filepath.Join(f.project, "NOTES"), []byte("todo\n"), 0o600))

	// act
	code, r := f.collect()

	// assert
	assert.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, "work by Someone\n", f.fetchClone(r))
	assert.Equal(t, "aibox: what the session left uncommitted by aibox\n", f.fetchEnd(r))
	assert.Equal(t, strings.TrimSpace(f.gitIn(f.project, "rev-parse", task.CloneBranch)), r.result.Head)
}

func TestCollectSendsNoBundleWithoutChanges(t *testing.T) {
	// arrange
	f := newCloneFixture(t)
	require.NoError(t, f.prepare(vm.CloneNew))

	// act
	code, r := f.collect()

	// assert
	assert.Equal(t, 0, code, r.result.Error)
	assert.Equal(t, []string{task.ResultFile}, r.names)
	assert.Equal(t, f.base, r.result.Head)
	assert.Contains(t, f.progress.String(), "the session made no changes\n")
}
