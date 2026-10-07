// Package task is what the host and the VM agree on for a task that runs
// unattended: the files the host gives it and the results it gives back.
//
// The host boots the VM with a blank state disk and the task share, and
// opens a session without a terminal. The results come as a tar archive on
// standard output, and standard error says what the task is doing. The host
// must read both, or the task stops on a full window. The exit code is 0
// when the task went through to its end and 1 when a step failed. An
// archive without ResultFile is a failure too.
//
// Everything that comes back was made by git and Claude Code running as the
// user of the VM, who may be hostile. Result is what the VM claims. The host
// checks the bundle against the commit it sent before it fetches anything,
// and treats every text as untrusted. The VM stops Claude Code after
// Settings.TimeoutSeconds and still sends what it did, but the host needs
// a limit of its own for a VM that hangs.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The files of the task share, which the host fills and the VM mounts
// read-only.
const (
	// SettingsFile holds the Settings as JSON.
	SettingsFile = "task.json"
	// PromptFile is what the task is to do, in words for Claude Code.
	PromptFile = "prompt.md"
	// InputBundle is a git bundle with the commit the task starts from as
	// HEAD.
	InputBundle = "input.bundle"
)

// The results, the files of the tar archive the VM sends back. All but
// ResultFile may be missing when the task failed before it made them.
const (
	// TranscriptFile is what Claude Code printed, as stream-json.
	TranscriptFile = "transcript.jsonl"
	// LogFile is what Claude Code printed to standard error.
	LogFile = "claude.log"
	// ChangesFile is a git bundle of Branch from Result.Base on. It is
	// missing when the task made no changes.
	ChangesFile = "changes.bundle"
	// ResultFile holds the Result as JSON. It comes last.
	ResultFile = "result.json"
)

// Branch is the branch the task works on and ChangesFile carries.
const Branch = "refs/heads/aibox/task"

// ErrBadSettings is a task.json the VM does not run.
var ErrBadSettings = errors.New("bad settings of the task")

// Settings are how Claude Code runs the task. A zero value leaves the
// default of Claude Code, or has no limit. GitName and GitEmail are who the
// commits of the task are by, aibox when they are empty.
type Settings struct {
	Model          string  `json:"model,omitempty"`
	MaxTurns       int     `json:"maxTurns,omitempty"`
	MaxBudgetUSD   float64 `json:"maxBudgetUSD,omitempty"`
	TimeoutSeconds int     `json:"timeoutSeconds,omitempty"`
	GitName        string  `json:"gitName,omitempty"`
	GitEmail       string  `json:"gitEmail,omitempty"`
}

// Check returns ErrBadSettings for a negative limit, a model Claude Code
// could take for an option, or a git identity with control characters.
func (s Settings) Check() error {
	switch {
	case s.MaxTurns < 0:
		return fmt.Errorf("%w: maxTurns is %d", ErrBadSettings, s.MaxTurns)
	case s.MaxBudgetUSD < 0:
		return fmt.Errorf("%w: maxBudgetUSD is %g", ErrBadSettings, s.MaxBudgetUSD)
	case s.TimeoutSeconds < 0:
		return fmt.Errorf("%w: timeoutSeconds is %d", ErrBadSettings, s.TimeoutSeconds)
	case strings.HasPrefix(s.Model, "-") || strings.ContainsFunc(s.Model, isSpaceOrControl):
		return fmt.Errorf("%w: model is %q", ErrBadSettings, s.Model)
	case strings.ContainsFunc(s.GitName, isControl):
		return fmt.Errorf("%w: gitName is %q", ErrBadSettings, s.GitName)
	case strings.ContainsFunc(s.GitEmail, isSpaceOrControl):
		return fmt.Errorf("%w: gitEmail is %q", ErrBadSettings, s.GitEmail)
	}

	return nil
}

func isSpaceOrControl(r rune) bool {
	return r == ' ' || isControl(r)
}

func isControl(r rune) bool {
	return r < ' ' || r == 0x7f
}

// Result is what the VM tells the host about the task.
type Result struct {
	// Base is the commit the task started from, Head the commit Branch
	// points at in the end. They are equal when the task made no changes.
	Base string `json:"base,omitempty"`
	Head string `json:"head,omitempty"`
	// ClaudeExitCode is missing when Claude Code did not run.
	ClaudeExitCode *int `json:"claudeExitCode,omitempty"`
	// ClaudeResult is the last line Claude Code printed when it is JSON,
	// the result event with the cost and why it stopped. It is here even
	// when TranscriptFile was cut off.
	ClaudeResult json.RawMessage `json:"claudeResult,omitempty"`
	// TimedOut is true when Claude Code was stopped after
	// Settings.TimeoutSeconds.
	TimedOut bool `json:"timedOut,omitempty"`
	// Leftovers is true when aibox committed changes the task had left
	// uncommitted. Files git ignores are not part of it.
	Leftovers bool `json:"leftovers,omitempty"`
	// Truncated names the results that were cut off at their size limit.
	Truncated []string `json:"truncated,omitempty"`
	// Warnings are failures the task went on after, such as leftovers it
	// could not commit.
	Warnings []string `json:"warnings,omitempty"`
	// Error is the failure that stopped the task, if any.
	Error string `json:"error,omitempty"`
}
