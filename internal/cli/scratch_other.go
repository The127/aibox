//go:build !windows

package cli

import "path/filepath"

// scratchRepository is where the repository the input bundle comes from
// goes: in the folder of the task, below which the helper lets git write.
func scratchRepository(dir string) (string, error) {
	return filepath.Join(dir, "input.git"), nil
}
