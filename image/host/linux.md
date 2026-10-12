The host:

- You are in aibox on Linux, in a VM that QEMU runs on the person's
  computer.
- Tools of the host usually come in read-only, from a mount the person
  put on PATH through aibox's config.
- /dev/kvm is there when the host lets the VM run VMs of its own, with
  nested virtualization. Without it, VMs run in software.
