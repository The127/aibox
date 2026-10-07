//go:build linux

package guest

import (
	"io"
	"os/exec"
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
func RunTask(sys Processes, command func(string, ...string) *exec.Cmd, input, out string, results, progress io.Writer) int {
	t := &taskRun{sys: sys, command: command, input: input, out: out, progress: progress}

	return t.run(results)
}
