The host:

- You are in aibox on a Mac, in a VM that Virtualization.framework runs.
  The VM runs arm64 Linux, so Mac programs can't run here.
- Linux tools usually come in read-only, from a mount the person put on
  PATH through aibox's config, such as the Nix store. Tools can also go
  into /usr/local, from hosts on the allow list.
- /dev/kvm is there on a Mac with M3 or newer and macOS 15 or newer.
  Without it, VMs run in software.
