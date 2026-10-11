package hvservice

import "github.com/Microsoft/go-winio/pkg/guid"

// ConsolePipe is the pipe the console of the VM of the id goes to, which
// the service makes before the VM, as MakeConsole does: Hyper-V makes a
// pipe that isn't there itself, as its own, which aibox couldn't read.
func ConsolePipe(id guid.GUID) string {
	return `\\.\pipe\aibox-console-` + id.String()
}
