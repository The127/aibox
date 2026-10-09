# How aibox works

aibox runs Claude Code inside a microVM, with the project folder shared into
it. This page describes Linux. [macOS](#macos) says what is different there.

## The VM

- The VM runs on QEMU's microvm machine type.
- The project folder is shared into the VM with virtio-fs. Your user on the
  host is the user inside the VM. The VM can write everything in the project,
  `.git` included. [What the VM writes](what-the-vm-writes.md) says
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
For `localhost` the proxy does not ask DNS. It dials the one loopback
address the allow list names for that port. So programs that ignore the proxy
variables reach the port too, and the server sees the `Host` it expects,
since many local servers refuse other names against DNS rebinding.

An API key of the Claude Console in `env` stays on the host. The VM gets a
placeholder and `ANTHROPIC_BASE_URL=http://127.0.0.1:3129`, and the init
listens on that port as on the others. For `CONNECT localhost:3129` the
proxy dials nothing and answers HTTP in the tunnel itself. It sends each
request under `/v1/`, and the check Claude Code makes at `/api/hello`, to
`https://api.anthropic.com`, whatever host the request names. It refuses a
path with dot segments or escapes, drops the credentials the VM sent and
adds the key. The log of the proxy shows these requests as `localhost:3129`.
aibox reads the root certificates before it is confined, because the sandbox
keeps it from reading them later, and on macOS from asking the system to
check a certificate. A subscription token is passed into the VM as it is.

The repositories that `git` in the config names go through a git broker on
the host. aibox asks git on the host for the login of each one before the VM
starts. It runs git outside any repository and without a terminal, so the
config of the project, which the VM can write, has no say. The VM gets a git
config, through `GIT_CONFIG_COUNT` and the variables that go with it, that
sends the HTTPS and SSH addresses of each repository to
`http://127.0.0.1:3130`, which the init forwards as the other ports. For
`CONNECT localhost:3130` the proxy serves the broker in the tunnel. The
broker speaks the smart HTTP protocol of git and accepts only its requests
for the repositories of the list, without dot segments or escapes. It sends
them to `https://HOST/PATH.git`, which the config fixes and not the request,
with the login of the host and with no credentials or cookies of the VM. It
refuses a fetch the config does not allow. Of a push it reads the commands
at the start, which name each ref with its old and new commit, and passes
the push on only when each one updates or creates a branch of the list, and
otherwise refuses all of them, since the pack belongs to the whole push. A
push larger than the buffer of git comes after a probe that is a flush
alone, which the broker passes on. It
refuses deletes, tags, signed pushes, push options and pushes from a shallow
clone, and answers in the format of git, so that git in the VM shows why. It
cannot tell a force push from another one, since that needs the history.
Answers that refuse the login or redirect become an error for the VM, so it
never asks for a login of its own.

When git on the host has no login for a repository, the broker reaches it
over SSH as `git@HOST`. Before the VM starts, aibox asks `ssh -G` for the
host name, the port, the key files, the agent and the known hosts ssh would
use, takes the keys of the agent and the key files without a passphrase
among them, and reads the known hosts. It refuses a host the config reaches
through a proxy. The connection to the agent stays open, since it signs for the
broker later, and the port of the server joins the ports aibox may connect
to. The broker asks the server only for the types of key the known hosts
hold for it, and refuses a server they do not know. It logs in once per
repository and opens a session on that connection for each request, at
most four at a time, and logs in again when the connection broke. HTTP is
stateless and SSH is not, so for each request of git in the VM the broker
runs the service on the server once. For the advertisement it passes on
what the server advertises, with the line that names the service over
HTTP, and then ends the service. For a request it skips the advertisement,
sends the body, decompressed when git compressed it for HTTP, and passes on
the answer. Git in the VM sends `Git-Protocol`, which the broker passes on
as `GIT_PROTOCOL` so that a fetch runs in version 2. Logging in, opening
and closing a session, the advertisement and each silence in an answer have
a time limit, a request git in the VM gives up on ends its session, and what the server says on its standard error goes into the
error and the log. The checks of a push are the same as over HTTPS. A
server path that starts with `-` or `~` is refused in the config, since the
server would read it as an option or a home folder.

The proxy logs each host and port it connected to or refused, once per run.
A task
has a log of its own, and its report names the hosts. The log holds the name
the VM put in its `CONNECT` and the port, not the address it reached behind
a name nor what went through, so it cannot tell a push from a fetch.

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

## Tasks

`aibox task` boots the same VM without a terminal. The kernel command line
tells the init that it runs a task.

- The host writes the prompt, the settings and a bundle of the commit the
  task starts from into the folder of the task, which the VM gets as a
  read-only share. git bundles refs only, so the bundle comes from an empty
  repository that borrows the objects of the project and has the commit as
  its `HEAD`. The project gets no new ref. The project folder and the home
  of the project are not shared. The mounts of the config and the skills of
  the person are, read-only as for `aibox run`.
- Each task gets a new state disk, which the init formats. The home and the
  project are folders on it. The host removes the file as soon as the VM has
  it open, so the disk is gone when the VM is. Tasks do not lock the project,
  so they run next to each other and next to `aibox run`.
- The init clones the bundle, removes the remote and switches to the branch
  `aibox/task`.
- It runs `claude --print` as the user, with `bypassPermissions`,
  `stream-json` and `image/task.md` as an extra system prompt.
- Then it commits what is left as a commit of its own and makes a bundle of
  the branch. aibox's git steps run with hooks and fsmonitor off, since the
  repository came from the task. Filters the task sets up still run, inside
  the VM.
- Each step runs in a new cgroup, and the init kills that cgroup when the
  step ends, so nothing a step started outlives it. The git steps before
  Claude Code have 10 minutes together, and the ones after it have 10 more.
  Claude Code has the timeout of the task. The host stops the VM 25 minutes
  after the timeout at the latest.
- The results come back over the SSH session, as a tar on its standard
  output. The init sends each file once it is complete and `result.json`
  last. Progress goes over its standard error.
- The host accepts only files it knows by name. Each must be a plain file,
  sent once, and within a size limit. When the host refuses the results, it
  stops the VM.
- The host reads the header of the bundle without git. It refuses a bundle
  that carries another ref than `aibox/task` or needs a commit other than the
  one the task started from. Text from the VM is cleaned of control
  characters before it reaches the terminal. Once a task has its ID, every
  line it prints starts with the time, `aibox` or `vm` for where it comes
  from, and the random end of the ID, as in `15:04:07 vm[a1b2c3]: cloning
  the input`. The host adds this to the lines from the VM, so they cannot
  pass for lines of aibox, and the lines of tasks that share a log can be
  told apart. The interactive session passes the terminal of the VM
  through, so this holds only for tasks.
- The files of the results are opened before the VM starts, since aibox
  confines itself once the VM runs and can open no file then. For the same
  reason a task cannot remove the bundle of its input when it ends. `aibox
  tasks clean` removes the bundles, or the folders, of the tasks that ended.
  Each task holds a lock on a file in its folder while it runs, and its
  folder gets its name only once it holds the lock, so the command leaves a
  task that runs alone.

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
- QEMU and virtiofsd should end with aibox, even when aibox is killed or
  crashes. aibox starts them with a parent death signal, so the kernel sends
  them SIGKILL when aibox dies. In the sandbox bubblewrap does the same for
  QEMU with --die-with-parent. The kernel sends that signal when the thread
  that started the program ends, so aibox starts all of them from one thread
  that lives as long as aibox does. virtiofsd forks the process that serves
  the share, which ends by a signal of its own. That this works with the
  real virtiofsd is not yet tested.
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
- The VM runs in a process of Virtualization.framework, which ends with
  aibox, also when aibox is killed. The shares are served by that process,
  so nothing is left either.
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
  [CONTRIBUTING.md](https://github.com/The127/aibox/blob/main/CONTRIBUTING.md#the-vm-image) says how.
- aibox is built with cgo and signed with the entitlement
  Virtualization.framework asks for. A build without cgo says that it runs a
  VM on macOS only when built with cgo.
