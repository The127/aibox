package hvlaunch

import (
	"github.com/the127/aibox/internal/hvservice"
)

// consoleOf is the console pipe under the handle the service put into
// this process.
func consoleOf(handle uint64) consoleSource {
	return hvservice.ConsoleOf(handle)
}
