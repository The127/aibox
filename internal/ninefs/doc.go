// Package ninefs serves folders of a Windows host to a Linux guest over
// 9P2000.L, as the user who runs it. Hyper-V's own Plan 9 server runs as
// the VM and needs the folders opened up to it; this one runs in the
// user's process, so the guest can do no more than the user could.
//
// It walks one name at a time from open handles and never follows a link
// or junction on the host, so nothing outside a share can be reached
// through it. It knows nothing of aibox.
package ninefs
