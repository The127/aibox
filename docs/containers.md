# Containers and VMs

Containers and VMs run inside the VM without root. podman and QEMU come from
your machine like other tools, through [`mounts`](config.md#mounts) and
[`path`](config.md#path).

- A container shares the network of the VM. It has only the loopback and
  the proxy, and gets the proxy variables. `preset:docker` allows pulls from
  Docker Hub.
- The storage of podman lives on the state disk under
  `~/.local/share/containers`, since overlayfs does not work on virtio-fs.
- Limits such as `--memory` and `--pids-limit` of podman apply.
- Setuid programs inside an image do not work, since the state disk is
  mounted nosuid.
- The VM has `/dev/kvm` for VMs of its own. On Linux this needs nested
  virtualization on the host. On macOS the VM has `/dev/kvm` where Virtualization.framework offers nested virtualization,
  which Apple documents for M3 and newer with macOS 15 or newer. Elsewhere
  such VMs run in software.

## How it works

The kernel has user namespaces, cgroups, overlayfs and KVM. The init opens
`/dev/kvm` and `/dev/fuse` to everyone and mounts cgroup2. The image has
`newuidmap` and `newgidmap` with a range of subordinate IDs for the user.
The init gives the user a cgroup with the controllers cpuset, cpu, io,
memory and pids, and starts the command in a cgroup below it.
