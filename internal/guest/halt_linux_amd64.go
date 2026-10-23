package guest

import "syscall"

// haltCommand resets the machine, which ends the VM, because QEMU runs with
// -no-reboot.
const haltCommand = syscall.LINUX_REBOOT_CMD_RESTART
