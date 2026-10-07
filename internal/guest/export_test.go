//go:build linux

package guest

import (
	"io"
	"os/exec"
	"time"

	"github.com/the127/aibox/internal/session"
)

// HaltCommand is how Halt asks the kernel to end the VM on this
// architecture.
const HaltCommand = haltCommand

// The programs a task runs, which the tests replace.
const (
	ClaudePath = claude
	GitPath    = gitPath
)

// RunTask runs a task with the programs command sets up, the task share in
// input and the results kept in out, and writes the results to results.
// The git steps before and after Claude Code have gitTimeout each.
func RunTask(sys Processes, command func(string, ...string) *exec.Cmd, input, out string, gitTimeout time.Duration, results, progress io.Writer) int {
	t := &taskRun{sys: sys, command: command, input: input, out: out, progress: progress, gitTimeout: gitTimeout}

	return t.run(results)
}

// StartTask starts a task like RunTask does, as the process of a session.
func StartTask(sys Processes, command func(string, ...string) *exec.Cmd, input, out string) session.Process {
	return startTask(&taskRun{sys: sys, command: command, input: input, out: out, progress: io.Discard, gitTimeout: gitTimeout})
}
