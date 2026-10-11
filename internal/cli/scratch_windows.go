package cli

import "os"

// scratchRepository is where the repository the input bundle comes from
// goes: in the temporary folder, not the task's, whose path can be longer
// than the 260 characters git for Windows takes for a --git-dir, with
// core.longpaths or without. Nothing confines git on Windows yet; once
// something does, it must let git write there too.
func scratchRepository(string) (string, error) {
	return os.MkdirTemp("", "aibox-input-")
}
