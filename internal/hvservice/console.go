package hvservice

import "github.com/Microsoft/go-winio/pkg/guid"

// ConsolePipe is the pipe the console of the VM of the id goes to. The
// service picks the id, a new GUID for each VM, so that no one can name a
// pipe of theirs for the VM to write its console to.
func ConsolePipe(id guid.GUID) string {
	return `\\.\pipe\aibox-console-` + id.String()
}
