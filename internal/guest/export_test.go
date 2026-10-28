//go:build linux

package guest

import (
	"io"
	"os/exec"
	"time"

	"github.com/the127/aibox/internal/session"
	"github.com/the127/aibox/internal/task"
)

// HaltCommand is how Halt asks the kernel to end the VM on this
// architecture.
const HaltCommand = haltCommand

// The programs a task runs, which the tests replace.
const (
	ClaudePath = claude
	GitPath    = gitPath
	MisePath   = mise
)

// ClipboardToolAt is ClipboardTool with the clipboard of the host at base.
var ClipboardToolAt = clipboardTool

// RunTask runs a task with the programs command sets up, the task share in
// input and the results kept in out, and writes the results to results.
// The git steps before and after Claude Code have gitTimeout each.
func RunTask(sys Processes, command func(string, ...string) *exec.Cmd, input, out string, gitTimeout time.Duration, results, progress io.Writer) int {
	t := testTask(&taskRun{sys: sys, command: command, input: input, out: out, progress: progress, gitTimeout: gitTimeout})

	return t.run(results, t.steps)
}

// RunTaskWithMise is RunTask with the install of mise before Claude Code.
func RunTaskWithMise(sys Processes, command func(string, ...string) *exec.Cmd, input, out string, results, progress io.Writer) int {
	t := testTask(&taskRun{sys: sys, command: command, input: input, out: out, progress: progress, gitTimeout: gitTimeout, mise: true})

	return t.run(results, t.steps)
}

// StartTask starts a task like RunTask does, as the process of a session.
func StartTask(sys Processes, command func(string, ...string) *exec.Cmd, input, out string) session.Process {
	t := testTask(&taskRun{sys: sys, command: command, input: input, out: out, progress: io.Discard, gitTimeout: gitTimeout})

	return startTask(t, t.steps)
}

// testTask gives the task what newTask gives it besides the fields of the
// tests.
func testTask(t *taskRun) *taskRun {
	t.branch, t.actor, t.stepName, t.ends = task.Branch, "the task", "step", []string{userCgroup, stepCgroup}

	return t
}

// PrepareClone makes or checks the clone like a session on a clone does
// before its command starts, with the programs command sets up and the
// clone share in input.
func PrepareClone(sys Processes, command func(string, ...string) *exec.Cmd, input, mode string) error {
	t := cloneRun(&taskRun{sys: sys, command: command, input: input, progress: io.Discard, gitTimeout: gitTimeout}, "clone")

	return t.prepareClone(mode)
}

// RunCollect sends what a session on a clone did, like the session after
// it does, with the results kept in out.
func RunCollect(sys Processes, command func(string, ...string) *exec.Cmd, input, out string, results, progress io.Writer) int {
	t := cloneRun(&taskRun{sys: sys, command: command, input: input, out: out, progress: progress, gitTimeout: gitTimeout}, "collect")

	return t.run(results, t.collect)
}

// Plan9Options is plan9Options.
var Plan9Options = plan9Options

// SCSIDiskIn is SCSIDisk with the folder of the SCSI disks and the wait of
// the tests.
func SCSIDiskIn(class string, lun int, wait time.Duration) (string, error) {
	return scsiDisk(class, lun, wait)
}

// DialPlan9 is dialPlan9.
var DialPlan9 = dialPlan9
