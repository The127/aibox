# How aibox works

aibox runs Claude Code inside a microVM, with the project folder shared into
it. This page describes Linux. [macOS](#macos) says what is different there.

## The VM

- The VM runs on QEMU's microvm machine type.
- The project folder is shared into the VM with virtio-fs. Your user on the
  host is the user inside the VM. The VM can write everything in the project,
  `.git` included. [What the VM writes](../README.md#what-the-vm-writes) says
  what that means for you.
- The root disk is read-only for QEMU and for the VM. The init puts an
  overlay in RAM over it only to make the mount points, then makes the root
  read-only again, so inside the VM only `/tmp`, `/run`, `/dev/shm`, the
  home, the project and the state disk take writes.
- The VM image (kernel and root disk) is built with
  [miso](https://github.com/The127/miso). The kernel has no PCI, no ACPI, no
  modules and no network drivers: `image/microvm.config` is a complete
  configuration for QEMU's microvm board, and `image/kernel.config` holds
  what aibox changes about it.

## Network

The VM has no network card. All traffic goes over vsock to a proxy on the
host, which only lets through the hosts on the allow list of the project.
Each VM gets a vsock namespace of its own, which aibox creates inside an
unprivileged user namespace, so no root is needed. Every VM can then use the
same address, CID 3, and other processes on the host cannot connect to it.

A port on the loopback of the host that the allow list names, such as
`127.0.0.1:8123`, is at the same port on the loopback of the VM. The init
learns the ports from the kernel command line, listens on `127.0.0.1` for
each and sends each connection to the proxy with `CONNECT localhost:PORT`.
For `localhost` the proxy does not ask DNS. It dials the loopback addresses
the allow list names for that port. So programs that ignore the proxy
variables reach the port too, and the server sees the `Host` it expects,
since many local servers refuse other names against DNS rebinding.

## Terminal

The terminal is an SSH session over vsock. The init of the VM runs Claude
Code on a pseudo terminal and serves it to aibox on the host, which puts
your terminal into raw mode and attaches it. The size of your terminal and
its changes reach the VM, and the exit code of Claude Code comes back. The
virtio console of the VM, with the messages of the kernel and of the init,
goes into `console.log` in the project's aibox folder.

## What a project keeps

- The settings of a project live outside the project, in
  `~/.aibox/projects/<escaped path>/config.yaml`, so the VM can't change
  them.
- The VM's home directory is `~/.aibox/projects/<escaped path>/home/`, shared
  into the VM over virtio-fs, so `~/.claude` and the login survive restarts.
- Each project has a disk of its own, `state.ext4` next to the home, that the
  VM keeps `/usr/local` and `~/.cache` on. Tools installed there survive
  restarts and rebuilds of the image, and caches of many small files, like
  the Go build cache or pip's, stay off the shared home, which is slow for
  them. The file is sparse and takes up host disk only as it fills. Its size
  is fixed when the disk is created on the first run. The init formats it on
  the first boot and refuses a disk whose superblock has data but no ext4
  magic, so a damaged disk is never formatted over.

## Containers and VMs inside the VM

Containers and VMs run inside the VM without root. The kernel has user
namespaces, cgroups, overlayfs and KVM, the init opens `/dev/kvm` and
`/dev/fuse` to everyone and mounts cgroup2, and the image has `newuidmap` and
`newgidmap` with a subuid range for the user. podman's storage lives on the
state disk under `~/.local/share/containers`, since overlayfs does not work
on virtio-fs. A container shares the network of the VM, so it has only
loopback and the proxy, and gets the proxy variables. `preset:docker` allows
pulls from Docker Hub. podman and QEMU themselves come from the host like
other tools, through `mounts` and `path`. The init gives the user a cgroup
with the controllers cpuset, cpu, io, memory and pids and starts the command
in a cgroup below it, so limits such as `--memory` and `--pids-limit` apply.
Setuid programs inside an image do not work, since the state disk is mounted
nosuid.

## The host side

- QEMU runs in a bubblewrap sandbox with no network, no environment and no
  writable file system. It sees only its own program, libraries and firmware
  and a copy of the kernel. Devices, the disks and the sockets reach it as
  open files from aibox, and it filters its own syscalls.
  `aibox run --no-sandbox` runs it without bubblewrap, for debugging.
- Once QEMU runs, aibox restricts itself too: no new privileges, Landlock
  rules that let it read `/etc` for DNS and connect only to the ports of the
  allow list, and a seccomp filter that refuses ptrace, mount, namespace and
  similar syscalls. This needs Linux 6.7 or newer.
- aibox refuses to start in your home folder, above it, or above `~/.aibox`.
  It compares folders rather than paths, so a symlink, another case on a file
  system that ignores case, or a bind mount does not get past it.

## macOS

On macOS aibox runs the VM with Virtualization.framework instead of QEMU. The
guest, the image layout, the commands and the config are the same as on
Linux. What is different:

- aibox owns the VM, as it owns QEMU on Linux. The terminal and the proxy are
  on vsock, which only the process that owns the VM can reach, so no vsock
  namespace is needed. The shares are virtio-fs as on Linux, read-only where
  Linux has them read-only, which the host enforces. There is no mapping of
  user ids: the VM user writes as you, and the files it makes are yours.
- The VM is arm64 Linux, so programs of the Mac do not run in it. A mount of a
  tool from the Mac, such as a Go SDK or `/nix/store`, gives the VM files it
  cannot run. Tools for the VM come from a folder of arm64 Linux programs, or
  are installed into `/usr/local` in the VM, which keeps them.
- On a Mac `/users/YOU` and `/System/Volumes/Data/Users/you` are your home
  folder too, and `/System/Volumes/Data` holds it. aibox refuses those as
  well.
- The VM can set and remove the extended attributes of files in the project,
  `com.apple.quarantine` among them, and the files it makes carry none, so
  Gatekeeper never checks a program that comes out of the VM.
  Virtualization.framework has no option against it. Like everything the VM
  writes, such a program is untrusted until you have read it.
- The state disk is the same sparse file. Virtualization.framework locks it
  for as long as the VM runs, which keeps a second run of the project off.
- Once the VM runs, aibox confines itself with Seatbelt, the sandbox of
  macOS, as it does with Landlock and seccomp on Linux. It reads the contents
  of no files but those of the resolver, though it sees the metadata of all,
  connects over TCP only to the ports of the allow list, reaches the name
  service of the system, starts no programs and cannot read or signal other
  processes. The files it opened before, such as the logs, and the VM keep
  working. The VM itself runs in a process of Virtualization.framework,
  which aibox cannot put into a sandbox of its own as it does QEMU with
  bubblewrap, so `--no-sandbox` changes nothing on macOS.
- The kernel is the same source for arm64. `image/vz-arm64.config` is the
  configuration of the kernel Apple's container tool boots on
  Virtualization.framework, and `image/kernel-arm64.config` holds what aibox
  changes about it, as `kernel.config` does for x86. Like the kernel for x86
  it has no modules, no network drivers, no `bpf()` syscall, no ftrace and no
  kprobes. Unlike it, it has PCI and ACPI.
- Where Virtualization.framework offers nested virtualization, which Apple
  documents for M3 and newer with macOS 15 or newer, the VM has `/dev/kvm`
  for VMs of its own, as on Linux. Elsewhere such VMs run in software.
- miso does not run on macOS yet, so the image for arm64 is built from
  `image/Containerfile`, whose stages follow the Imagefile.
  [CONTRIBUTING.md](../CONTRIBUTING.md#the-vm-image) says how.
- aibox is built with cgo and signed with the entitlement
  Virtualization.framework asks for. A build without cgo says that it runs a
  VM on macOS only when built with cgo.
