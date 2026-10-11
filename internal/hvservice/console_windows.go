package hvservice

import "fmt"

// ConsoleSDDL is the security of the pipe aibox listens on for the
// console of its VM: the user may do all with it, as they make it, and
// Hyper-V may connect the VM to it, as SYSTEM or as the VM. No one else
// may read the console.
func ConsoleSDDL(user string) (string, error) {
	if !sidPattern.MatchString(user) {
		return "", fmt.Errorf("%w: %q", ErrBadSID, user)
	}

	return fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;%s)(A;;0x%X;;;%s)", user, pipeUse, VirtualMachines), nil
}

// VirtualMachines is the group of the accounts the VMs of Hyper-V run as.
// The pipe lets the group in rather than the VM's own account, which
// exists only while the VM does, and the pipe is made before it: a VM of
// someone else would have to guess the name, and aibox takes only the
// first connection.
const VirtualMachines = "S-1-5-83-0"
