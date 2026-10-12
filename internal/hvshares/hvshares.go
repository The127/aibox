// Package hvshares is the helper of aibox on Windows that serves the VM
// the folders it shares, over 9p on a Hyper-V socket of the VM, in a
// process of its own: the work that faces the VM, but needs the files of
// the user, apart from the work that faces the VM and needs none of them,
// which runs in an AppContainer. aibox starts it once the VM is made,
// and it ends when aibox does.
package hvshares

// Command is the hidden command of aibox that runs the helper.
const Command = "serve-shares"
