package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// readTimeout is how long a program may take to read the clipboard. The
// clipboard is on this machine, so it answers at once or not at all, and a
// read that hangs would hold up every later Ctrl+V.
const readTimeout = 10 * time.Second

// output runs the program that reads the clipboard and returns what it
// printed.
func output(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // each reader passes a fixed program
	// a child the program left behind may hold its output open
	cmd.WaitDelay = time.Second

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, failure(err))
	}

	return out, nil
}

// failure adds what a program printed on standard error to its exit error.
func failure(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(exitErr.Stderr))
	}

	return err
}
