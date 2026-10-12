// Package hvvm is the helper of aibox on Windows that serves a VM its
// proxy from an AppContainer: the parsing of what the VM sends through
// it, which a VM may try to take over, in a process that can read none of
// the files of the user, holds no secret, has no console and reaches no
// more of the network than the allow list. The services the proxy answers
// itself it hands to aibox, which holds their secrets.
package hvvm

// Command is the hidden command of aibox that runs the helper, with the
// numbers of the handles of the pipe from aibox and the one to it.
const Command = "serve-vm"
