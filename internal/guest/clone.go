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
		return task.Clone{}, fmt.Errorf("read the settings of the clone: %w", err)
	}

	return clone, clone.Check()
}

// prepareClone makes a new clone in the project, or checks that the one an
// earlier session left is there.
func (t *taskRun) prepareClone(mode string) error {
	t.gitDeadline = time.Now().Add(t.gitTimeout)

	if mode == vm.CloneKeep {
		if _, err := t.commit("HEAD"); err != nil {
			return fmt.Errorf("there is no clone to go on with: %w", err)
		}

		return nil
	}

	clone, err := readClone(filepath.Join(t.input, task.CloneFile))
	if err != nil {
		return err
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

	if err := t.sys.Kill(userCgroup); err != nil {
		return fmt.Errorf("end what the session left running: %w", err)
	}

	t.gitDeadline = time.Now().Add(t.gitTimeout)

	return t.sendChanges(clone.Base)
}
