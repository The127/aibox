// Package hvlaunch runs the aibox VM on Hyper-V, through the Windows
// service of aibox, as launch runs it with QEMU on Linux: aibox serves
// the shares itself, over 9p on Hyper-V sockets, and the terminal and the
// proxy are on ports of the VM only aibox can listen on. The service
// makes and ends the VM, as Hyper-V serves only Administrators, and gives
// it the state disk aibox holds open for it.
package hvlaunch
