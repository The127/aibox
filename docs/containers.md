# Containers and VMs

You can run containers and VMs inside the VM, without root. Bring podman or
QEMU from your machine like any other tool, with
[`mounts`](config.md#mounts) and [`path`](config.md#path).

A container shares the VM's network. It only has the loopback and the proxy,
and gets the proxy variables. To pull images from Docker Hub, add
`preset:docker` to the allow list.

podman keeps its storage on the state disk, in
`~/.local/share/containers`. overlayfs doesn't work on virtio-fs, so it
can't live in the shared home.

podman's limits, like `--memory` and `--pids-limit`, work. Setuid programs
in an image don't, because the state disk is mounted with nosuid.

## VMs

The VM has `/dev/kvm` for VMs of its own. On Linux this needs nested
virtualization on your machine. On macOS it needs a Mac where
Virtualization.framework offers nested virtualization. Apple documents that
for M3 and newer, with macOS 15 or newer. Everywhere else, those VMs run in
software.

## How it works

The VM's kernel has user namespaces, cgroups, overlayfs and KVM. The init
opens `/dev/kvm` and `/dev/fuse` to everyone and mounts cgroup2. The image
has `newuidmap` and `newgidmap`, with a range of subordinate IDs for the
user. The init gives the user a cgroup with the cpuset, cpu, io, memory and
pids controllers, and starts the command in a cgroup below it.
