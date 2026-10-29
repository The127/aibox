//go:build linux

package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/the127/aibox/internal/session"
	"github.com/the127/aibox/internal/task"
	"github.com/the127/aibox/internal/vm"
)

const (
	// the clone share holds what the host gives a session on a clone
	cloneShare = "clone"
	cloneDir   = "/var/lib/aibox/clone"
)

// newCloneRun is the steps of a session on a clone, which run like those
// of a task, as the user. Only the cgroups of the steps are ended between
// them, since the session on the terminal runs in a cgroup of the user
// before the steps that come after it.
func newCloneRun(sys Processes, options Options, request session.Request, stepName string) *taskRun {
	t := newTask(sys, options, request)
	t.input = cloneDir

	return cloneRun(t, stepName)
}

// cloneRun turns the steps of a task into those of a session on a clone.
func cloneRun(t *taskRun, stepName string) *taskRun {
	t.branch = task.CloneBranch
	t.actor = "the session"
	t.stepName = stepName
	t.ends = []string{stepCgroup}

	return t
}

// readClone reads clone.json.
func readClone(path string) (task.Clone, error) {
	var clone task.Clone

	if err := readStrict(path, &clone); err != nil {
		return task.Clone{}, fmt.Errorf("read %s: %w", task.CloneFile, err)
	}

	if err := clone.Check(); err != nil {
		return task.Clone{}, fmt.Errorf("read %s: %w", task.CloneFile, err)
	}

	return clone, nil
}

// prepareClone makes a new clone in the project, or checks that the one an
// earlier session left is there and started from the base, which the
// bundle at the end starts from.
func (t *taskRun) prepareClone(mode string) error {
	t.gitDeadline = time.Now().Add(t.gitTimeout)

	clone, err := readClone(filepath.Join(t.input, task.CloneFile))
	if err != nil {
		return err
	}

	if mode == vm.CloneKeep {
		if _, err := t.commit("HEAD"); err != nil {
			return fmt.Errorf("there is no clone to go on with: %w", err)
		}

		if err := t.git(nil, "merge-base", "--is-ancestor", clone.Base, "HEAD"); err != nil {
			return fmt.Errorf("the clone did not start from %s: %w", clone.Base, err)
		}

		return nil
	}

	bundle := filepath.Join(t.input, task.InputBundle)
	if err := t.git(nil, "-c", "advice.detachedHead=false", "clone", "--quiet", "--", bundle, "."); err != nil {
		return fmt.Errorf("clone the input: %w", err)
	}

	// the bundle is no remote anyone can use
	if err := t.git(nil, "remote", "remove", "origin"); err != nil {
		return fmt.Errorf("remove the remote of the input: %w", err)
	}

	for _, remote := range clone.Remotes {
		if err := t.git(nil, "remote", "add", "--", remote.Name, remote.URL); err != nil {
			return fmt.Errorf("add the remote %s: %w", remote.Name, err)
		}
	}

	branch := strings.TrimPrefix(t.branch, "refs/heads/")
	if err := t.git(nil, "switch", "--quiet", "--create", branch); err != nil {
		return fmt.Errorf("make the branch %s: %w", branch, err)
	}

	head, err := t.commit("HEAD")
	if err != nil {
		return err
	}

	if head != clone.Base {
		return fmt.Errorf("the input starts at %s, not at the base %s", head, clone.Base)
	}

	return nil
}

// collect ends what the session left running, which could change the
// clone while it is bundled, and sends what the session did.
func (t *taskRun) collect() error {
	clone, err := readClone(filepath.Join(t.input, task.CloneFile))
	if err != nil {
		return err
	}

	t.result.Base = clone.Base

	if err := os.MkdirAll(t.out, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", t.out, err)
	}

	// the commits are worth sending even when something still runs
	if err := t.sys.Kill(userCgroup); err != nil {
		warning := fmt.Sprintf("end what the session left running: %v", err)
		t.result.Warnings = append(t.result.Warnings, warning)
		t.say("%s\n", warning)
	}

	t.gitDeadline = time.Now().Add(t.gitTimeout)

	return t.sendClone(clone.Base)
}

// sendClone commits what was left uncommitted and keeps a bundle of the
// branch of the clone from the base on. When the session ended on another
// branch, the branch of the clone moves to its last commit if that only
// goes further, and else the bundle carries that commit as
// task.CloneEndBranch too, so that no commit is left behind.
func (t *taskRun) sendClone(base string) error {
	if err := t.commitLeftovers(); err != nil {
		warning := fmt.Sprintf("commit what %s left uncommitted: %v", t.actor, err)
		t.result.Warnings = append(t.result.Warnings, warning)
		t.say("%s\n", warning)
	}

	end, err := t.commit("HEAD")
	if err != nil {
		return err
	}

	branches := []string{t.branch}

	branch, err := t.commit(t.branch)

	switch {
	case err != nil, t.isAncestor(branch, end):
		if err := t.git(nil, "update-ref", t.branch, end); err != nil {
			return fmt.Errorf("point %s at the last commit: %w", t.branch, err)
		}

		branch = end
	case !t.isAncestor(end, branch):
		if err := t.git(nil, "update-ref", task.CloneEndBranch, end); err != nil {
			return fmt.Errorf("point %s at the last commit: %w", task.CloneEndBranch, err)
		}

		branches = append(branches, task.CloneEndBranch)
	}

	t.result.Head = branch

	if len(branches) == 1 && branch == base {
		t.say("%s made no changes\n", t.actor)

		return nil
	}

	t.say("bundling the changes\n")

	return t.bundle(base, branches...)
}

// isAncestor says whether the commit a is b or comes before it.
func (t *taskRun) isAncestor(a, b string) bool {
	return t.git(nil, "merge-base", "--is-ancestor", a, b) == nil
}
