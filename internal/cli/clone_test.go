package cli

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/task"
)

func (f *taskFixture) clone(args ...string) error {
	return f.run(append([]string{"--image", f.image, "--mode", "clone"}, args...)...)
}

// cloneSends makes the VM send the results after the session, by name in
// that order.
func (f *taskFixture) cloneSends(results map[string]string, names ...string) {
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		spec.OnAttach()

		if spec.Then != nil {
			writeResults(spec.Then.Stdout, results, names)
		}

		return nil
	}
}

// cloneSendsNothing makes the VM connect and end without results, as one
// does whose session broke off.
func (f *taskFixture) cloneSendsNothing() {
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		spec.OnAttach()

		return nil
	}
}

// writeResults writes the archive of results, until a write fails, as one
// does once aibox refuses the results.
func writeResults(w io.Writer, results map[string]string, names []string) {
	archive := tar.NewWriter(w)

	for _, name := range names {
		if archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(results[name]))}) != nil {
			return
		}

		if _, err := archive.Write([]byte(results[name])); err != nil {
			return
		}
	}

	_ = archive.Close()
}

// cloneChanges is the header of a bundle of the refs from base on.
func cloneChanges(base string, refs map[string]string) string {
	header := "# v2 git bundle\n-" + base + " one\n"
	for _, ref := range []string{task.CloneBranch, task.CloneEndBranch} {
		if commit, ok := refs[ref]; ok {
			header += commit + " " + ref + "\n"
		}
	}

	return header + "\nPACK"
}

// cloneSucceeds makes the VM send a bundle of otherCommit on the branch of
// the clone from base on.
func (f *taskFixture) cloneSucceeds(base string) {
	f.cloneSends(map[string]string{
		task.ChangesFile: cloneChanges(base, map[string]string{task.CloneBranch: otherCommit}),
		task.ResultFile:  `{"base":"` + base + `","head":"` + otherCommit + `"}`,
	}, task.ChangesFile, task.ResultFile)
}

// cloneDirs are the folders of the sessions on a clone, oldest first.
func (f *taskFixture) cloneDirs(t *testing.T) []string {
	t.Helper()

	dirs, err := filepath.Glob(filepath.Join(f.project(t).Dir, clonesFolder, "*"))
	require.NoError(t, err)

	return dirs
}

// cloneSettings is the clone.json the VM got.
func (f *taskFixture) cloneSettings(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(f.launch.spec.Clone, task.CloneFile))
	require.NoError(t, err)

	return string(content)
}

func TestRunOnACloneSharesTheCloneAndNotTheProject(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSucceeds(f.base)

	// act
	err := f.clone()

	// assert
	require.NoError(t, err)

	spec := f.launch.spec
	dirs := f.cloneDirs(t)
	require.Len(t, dirs, 1)
	assert.Equal(t, filepath.Join(dirs[0], "share"), spec.Clone)
	assert.False(t, spec.KeepClone)
	assert.Empty(t, spec.Project)
	assert.Equal(t, f.project(t).Home, spec.Home)
	assert.NotContains(t, spec.Read, f.cwd)
	require.NotNil(t, spec.Then)
	assert.Equal(t, cloneTimeout, spec.Then.Timeout)
	assert.JSONEq(t, `{"base":"`+f.base+`"}`, f.cloneSettings(t))

	input, err := os.Open(filepath.Join(spec.Clone, task.InputBundle))
	require.NoError(t, err)

	defer func() { _ = input.Close() }()

	bundle, err := task.ReadBundle(input)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"HEAD": f.base}, bundle.Refs)
}

func TestRunOnACloneSaysHowToFetchItsCommits(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSucceeds(f.base)

	// act
	err := f.clone()

	// assert
	require.NoError(t, err)

	dir := f.cloneDirs(t)[0]
	id := filepath.Base(dir)
	assert.Regexp(t, stamped(dir, "aibox",
		"the changes end at "+short(otherCommit)+", fetch them with",
		"  git -c transfer.fsckObjects=true fetch "+shellQuote(filepath.Join(dir, task.ChangesFile))+" aibox/clone:aibox/clone-"+id,
	), f.stderr.String())
}

func TestRunOnACloneFetchesTheBranchTheSessionEndedOnToo(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	end := "3333333333333333333333333333333333333333"
	f.cloneSends(map[string]string{
		task.ChangesFile: cloneChanges(f.base, map[string]string{task.CloneBranch: otherCommit, task.CloneEndBranch: end}),
		task.ResultFile:  `{"base":"` + f.base + `","head":"` + otherCommit + `"}`,
	}, task.ChangesFile, task.ResultFile)

	// act
	err := f.clone()

	// assert
	require.NoError(t, err)

	dir := f.cloneDirs(t)[0]
	id := filepath.Base(dir)
	assert.Contains(t, f.stderr.String(), "and the session ended apart from them at "+short(end))
	assert.Contains(t, f.stderr.String(), " aibox/clone:aibox/clone-"+id+" aibox/clone-end:aibox/clone-end-"+id+"\n")
}

func TestRunOnACloneRefusesABundleThatIsNotTheBranchesFromTheBase(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSends(map[string]string{
		task.ChangesFile: changes(f.base, otherCommit),
		task.ResultFile:  `{"base":"` + f.base + `","head":"` + otherCommit + `"}`,
	}, task.ChangesFile, task.ResultFile)

	// act
	err := f.clone()

	// assert
	require.ErrorIs(t, err, task.ErrBadBundle)
	assert.NotContains(t, f.stderr.String(), "fetch them with")
}

func TestRunOnACloneSaysWhenItsCommitsDidNotComeBack(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSendsNothing()

	// act
	err := f.clone()

	// assert
	require.ErrorIs(t, err, task.ErrBadResults)
	assert.Contains(t, f.stderr.String(), "--clone-keep goes on with it")
}

func TestRunOnACloneStopsWhenTheCommitsOfTheLastOneDidNotComeBack(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSendsNothing()
	require.Error(t, f.clone())
	f.launch.called = false

	// act
	err := f.clone()

	// assert
	var left cloneLeftError
	require.ErrorAs(t, err, &left)
	assert.Equal(t, f.cloneDirs(t)[0], left.dir)
	assert.False(t, f.launch.called)
}

func TestRunOnACloneGoesOnWithTheLastCloneWithCloneKeep(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSendsNothing()
	require.Error(t, f.clone())
	f.git(t, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--allow-empty", "--message", "two")
	f.cloneSucceeds(f.base)

	// act
	err := f.clone("--clone-keep")

	// assert
	require.NoError(t, err)
	assert.True(t, f.launch.spec.KeepClone)
	assert.JSONEq(t, `{"base":"`+f.base+`"}`, f.cloneSettings(t), "the base of the last clone, not HEAD")
	assert.NoFileExists(t, filepath.Join(f.launch.spec.Clone, task.InputBundle))
	assert.Len(t, f.cloneDirs(t), 2)
}

func TestRunOnACloneStartsANewCloneWithCloneDiscard(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSendsNothing()
	require.Error(t, f.clone())
	f.cloneSucceeds(f.base)

	// act
	err := f.clone("--clone-discard")

	// assert
	require.NoError(t, err)
	assert.False(t, f.launch.spec.KeepClone)
	assert.FileExists(t, filepath.Join(f.launch.spec.Clone, task.InputBundle))
}

func TestRunOnACloneStartsANewCloneWhenTheLastOneCameBack(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSucceeds(f.base)
	require.NoError(t, f.clone())

	// act
	err := f.clone()

	// assert
	require.NoError(t, err)
	assert.False(t, f.launch.spec.KeepClone)
	assert.Len(t, f.cloneDirs(t), 2)
}

func TestRunOnACloneCountsASessionWithoutChangesAsCameBack(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSends(map[string]string{task.ResultFile: `{"base":"` + f.base + `","head":"` + f.base + `"}`}, task.ResultFile)
	require.NoError(t, f.clone())

	// act
	err := f.clone()

	// assert
	require.NoError(t, err)
}

func TestRunOnACloneHasNoCloneToKeepAtFirst(t *testing.T) {
	// arrange
	f := newTaskFixture(t)

	// act
	err := f.clone("--clone-keep")

	// assert
	require.ErrorIs(t, err, errNoCloneToKeep)
}

func TestRunOnACloneStartsFromTheCommitOfFrom(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.git(t, "-c", "user.name=Someone", "-c", "user.email=someone@example.com", "commit", "--quiet", "--allow-empty", "--message", "two")
	f.cloneSucceeds(f.base)

	// act
	err := f.clone("--from", "HEAD~1")

	// assert
	require.NoError(t, err)
	assert.JSONEq(t, `{"base":"`+f.base+`"}`, f.cloneSettings(t))
}

func TestRunOnACloneGivesTheCloneTheRemotesTheVMCanUse(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.git(t, "remote", "add", "origin", "https://github.com/owner/repo.git")
	f.git(t, "remote", "add", "fork", "git@github.com:me/repo.git")
	f.git(t, "remote", "add", "token", "https://x:secret@github.com/owner/repo.git")
	f.git(t, "remote", "add", "local", "/home/me/repo")
	f.cloneSucceeds(f.base)

	// act
	err := f.clone()

	// assert
	require.NoError(t, err)
	assert.JSONEq(t, `{"base":"`+f.base+`","remotes":[{"name":"origin","url":"https://github.com/owner/repo.git"},{"name":"fork","url":"git@github.com:me/repo.git"}]}`, f.cloneSettings(t))
	assert.Contains(t, f.stderr.String(), "aibox: the clone gets no remote token,")
	assert.Contains(t, f.stderr.String(), "aibox: the clone gets no remote local,")
	assert.NotContains(t, f.stderr.String(), "secret")
}

func TestRunOnACloneTurnsTheIDELinkOff(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.writeConfig(t, "ide: true\nmode: clone\n")
	f.cloneSucceeds(f.base)

	// act
	err := f.run("--image", f.image)

	// assert
	require.NoError(t, err)
	assert.NotEmpty(t, f.launch.spec.Clone, "the mode of the config")
	assert.Contains(t, f.stderr.String(), "aibox: the IDE link is off with --mode clone")
}

func TestRunOnAKeptCloneInstallsNoToolsOfMise(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSendsNothing()
	require.Error(t, f.clone())
	f.writeConfig(t, "mise: true\n")
	f.cloneSucceeds(f.base)

	// act
	err := f.clone("--clone-keep")

	// assert
	require.NoError(t, err)
	assert.False(t, f.launch.spec.Mise)
	assert.Contains(t, f.stderr.String(), "aibox: the clone of the last session starts without the tools of mise")
}

func TestRunRefusesTheFlagsOfACloneWithoutTheModeClone(t *testing.T) {
	tests := map[string]struct {
		args []string
		want error
	}{
		"--clone-keep":               {[]string{"--clone-keep"}, errCloneFlags},
		"--clone-discard":            {[]string{"--clone-discard"}, errCloneFlags},
		"--from":                     {[]string{"--from", "main"}, errCloneFlags},
		"--mode mount --from":        {[]string{"--mode", "mount", "--from", "main"}, errCloneFlags},
		"keep and discard":           {[]string{"--mode", "clone", "--clone-keep", "--clone-discard"}, errKeepDiscard},
		"keep and from":              {[]string{"--mode", "clone", "--clone-keep", "--from", "main"}, errKeepFrom},
		"an unknown --mode":          {[]string{"--mode", "copy"}, nil},
		"a --from that is no commit": {[]string{"--mode", "clone", "--from", "nowhere"}, errCloneCommit},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newTaskFixture(t)

			// act
			err := f.run(append([]string{"--image", f.image}, test.args...)...)

			// assert
			require.Error(t, err)

			if test.want != nil {
				assert.ErrorIs(t, err, test.want)
			} else {
				assert.True(t, strings.Contains(err.Error(), "--mode"), err.Error())
			}

			assert.False(t, f.launch.called)
		})
	}
}

func TestRunOnACloneWhoseVMNeverConnectedLeavesTheLastCloneAsTheLast(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.cloneSucceeds(f.base)
	require.NoError(t, f.clone())

	f.launch.vm = nil
	f.launch.err = errors.New("the image is broken")
	require.ErrorContains(t, f.clone(), "the image is broken")
	assert.NotContains(t, f.stderr.String(), "--clone-keep")

	f.launch.err = nil
	f.cloneSucceeds(f.base)

	// act
	err := f.clone()

	// assert
	require.NoError(t, err, "the first session came back, the second never ran")
	assert.False(t, f.launch.spec.KeepClone)
}

func TestRunOnACloneSaysWhyTheVMStoppedBeforeWhatIsMissing(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.launch.vm = func(_ context.Context, spec backend.Spec) error {
		spec.OnAttach()

		return errors.New("the VM took longer than 15m0s after the session\x1b[2J")
	}

	// act
	err := f.clone()

	// assert
	require.ErrorIs(t, err, task.ErrBadResults)

	stderr := f.stderr.String()
	took := strings.Index(stderr, "the VM took longer than 15m0s after the session?[2J")
	missing := strings.Index(stderr, "the commits of the clone did not come back")
	assert.True(t, took >= 0 && missing > took, stderr)
}

func TestRunOnACloneTreatsALastCloneFileItCannotReadAsNotCameBack(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.writeConfig(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(f.project(t).Dir, lastCloneFile), []byte("../../elsewhere\n"), 0o600))

	// act
	err := f.clone()

	// assert
	var left cloneLeftError
	require.ErrorAs(t, err, &left)
	assert.Equal(t, filepath.Join(f.project(t).Dir, lastCloneFile), left.dir)
	require.ErrorIs(t, f.clone("--clone-keep"), errUnknownStart)
}

func TestRunOnACloneNamesTheNewSessionBeforeTheVMRuns(t *testing.T) {
	// arrange: an aibox killed during the session writes nothing after it
	f := newTaskFixture(t)
	f.cloneSucceeds(f.base)
	require.NoError(t, f.clone())

	first := filepath.Base(f.cloneDirs(t)[0])

	var named string

	f.launch.vm = func(context.Context, backend.Spec) error {
		content, err := os.ReadFile(filepath.Join(f.project(t).Dir, lastCloneFile))
		require.NoError(t, err)

		named = strings.TrimSpace(string(content))

		return errors.New("killed")
	}

	// act
	_ = f.clone()

	// assert
	dirs := f.cloneDirs(t)
	require.Len(t, dirs, 2)
	assert.NotEqual(t, first, named)
	assert.Contains(t, []string{filepath.Base(dirs[0]), filepath.Base(dirs[1])}, named)
}

func TestRunOnACloneWhoseFirstVMNeverConnectedLeavesNoLastClone(t *testing.T) {
	// arrange
	f := newTaskFixture(t)
	f.launch.err = errors.New("the image is broken")
	require.Error(t, f.clone())

	f.launch.err = nil
	f.cloneSucceeds(f.base)

	// act
	err := f.clone()

	// assert
	require.NoError(t, err)
}

func TestRunOnACloneKeepsNamingADiscardedSessionWhoseVMNeverConnected(t *testing.T) {
	// arrange: the VM may have replaced the discarded clone before it
	// failed, so the old session is not named again
	f := newTaskFixture(t)
	f.cloneSendsNothing()
	require.Error(t, f.clone())

	first := filepath.Base(f.cloneDirs(t)[0])

	f.launch.vm = nil
	f.launch.err = errors.New("the VM broke")
	require.Error(t, f.clone("--clone-discard"))

	// act
	content, err := os.ReadFile(filepath.Join(f.project(t).Dir, lastCloneFile))

	// assert
	require.NoError(t, err)
	assert.NotEqual(t, first, strings.TrimSpace(string(content)))
	assert.Len(t, content, lastCloneBytes)
}
