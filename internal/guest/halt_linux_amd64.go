package guest

import "syscall"

// haltCommand resets the machine: the kernel of the x86 microvm has no ACPI
// to power off with, and QEMU runs with -no-reboot, so a reset ends the VM.
const haltCommand = syscall.LINUX_REBOOT_CMD_RESTART
