package guest

import "syscall"

// haltCommand powers the machine off, which arm64 can do through PSCI.
// Virtualization.framework of macOS keeps a VM running that resets.
const haltCommand = syscall.LINUX_REBOOT_CMD_POWER_OFF
