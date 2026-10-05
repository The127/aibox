//go:build linux

package guest

// HaltCommand is how Halt asks the kernel to end the VM on this
// architecture.
const HaltCommand = haltCommand

// ErrSymlink is how Pin refuses a symlink.
var ErrSymlink = errSymlink
