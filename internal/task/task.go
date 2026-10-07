// Package task is what the host and the VM agree on for a task that runs
// unattended: the files the host gives it and the results it gives back.
package task

import (
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
// default of Claude Code.
type Settings struct {
	Model        string  `json:"model,omitempty"`
	MaxTurns     int     `json:"maxTurns,omitempty"`
	MaxBudgetUSD float64 `json:"maxBudgetUSD,omitempty"`
}

// Check returns ErrBadSettings for a negative limit, or a model Claude Code
// could take for an option.
func (s Settings) Check() error {
	switch {
	case s.MaxTurns < 0:
		return fmt.Errorf("%w: maxTurns is %d", ErrBadSettings, s.MaxTurns)
	case s.MaxBudgetUSD < 0:
		return fmt.Errorf("%w: maxBudgetUSD is %g", ErrBadSettings, s.MaxBudgetUSD)
	case strings.HasPrefix(s.Model, "-") || strings.ContainsFunc(s.Model, isSpaceOrControl):
		return fmt.Errorf("%w: model is %q", ErrBadSettings, s.Model)
	}

	return nil
}

func isSpaceOrControl(r rune) bool {
	return r <= ' ' || r == 0x7f
}

// Result is what the VM tells the host about the task.
type Result struct {
	// Base is the commit the task started from, Head the commit Branch
	// points at in the end. They are equal when the task made no changes.
	Base string `json:"base,omitempty"`
	Head string `json:"head,omitempty"`
	// ClaudeExitCode is missing when Claude Code did not run.
	ClaudeExitCode *int `json:"claudeExitCode,omitempty"`
	// Leftovers is true when aibox committed changes the task had left
	// uncommitted.
	Leftovers bool `json:"leftovers,omitempty"`
	// Truncated names the results that were cut off at their size limit.
	Truncated []string `json:"truncated,omitempty"`
	// Warnings are failures the task went on after.
	Warnings []string `json:"warnings,omitempty"`
	// Error is the failure that stopped the task, if any.
	Error string `json:"error,omitempty"`
}
