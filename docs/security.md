# Security

aibox runs Claude Code, which a prompt or the content of a repository can
steer. So aibox treats everything in the VM as untrusted: Claude Code, the
programs it runs and what they write.

## What aibox protects

- **Your other files.** With `aibox run` the VM sees the project folder.
  A task sees only a clone of a commit. Both see the folders `mounts` names
  and your skills in `~/.claude/skills`, read-only, and no other files of
  yours. aibox refuses to start in your home folder or above it.
- **The network.** The VM has no network card. It reaches only the hosts on
  the allow list, through a proxy on your machine. A name that resolves into
  the host's own networks is refused, unless the list names that address
  itself. A port on the loopback of your machine is reached only when the
  list names it. See [allow](config.md#allow).
- **An API key.** `ANTHROPIC_API_KEY` stays on your machine. The VM gets a
  placeholder, and aibox adds the key to the requests that go to the Claude
  API. See [Login](tasks.md#login).
- **Your git login.** The login stays on your machine. A broker adds it to
  the requests of git in the VM, and only for the repositories and branches
  the config names. See [Git](git.md).
- **Your machine, from the programs that run the VM.** On Linux QEMU runs in
  a bubblewrap sandbox, and aibox confines itself with Landlock and seccomp.
  On macOS aibox confines itself with Seatbelt. See
  [The host side](#the-host-side).
- **The VM image.** aibox takes only the image whose SHA-256 its release
  built into it.

## What aibox does not protect

- **What the VM writes into the project.** That includes `.git`, and files
  that tools on your machine run without asking. Read
  [What the VM writes](what-the-vm-writes.md).
- **What you allow.** The VM can reach every host of the allow list and
  every port of your machine the list names, use every secret of `env`, and
  push to every branch the `git` list names. A subscription token goes into
  the VM as it is, so the VM can read it. A task uses all of this with
  nobody there to stop it. See [Tasks](tasks.md).
- **What goes through an allowed connection.** `proxy.log` names the host
  and port the VM asked for. It does not name the address behind the host,
  nor what was sent. A push to `github.com` and a fetch from it look the
  same.
- **What the task says about itself.** The last message of Claude Code,
  `result.json` and the authors of the commits come from the VM.
- **Force pushes and CI.** The git broker cannot tell a force push to an
  allowed branch from another push. A pushed branch can start the CI of the
  repository, with its secrets. See
  [What the broker cannot stop](git.md#what-the-broker-cannot-stop).
- **Spending.** The only limit on time or spending aibox enforces itself is
  `--timeout` of a task. Give an API key a spend limit in the Console.
- **Gatekeeper on macOS.** The VM can set and remove the extended attributes
  of files in the project, `com.apple.quarantine` among them, and the files
  it makes carry none. So Gatekeeper never checks a program that comes out of
  the VM. Virtualization.framework has no option against it. Like
  everything the VM writes, such a program is untrusted until you have read
  it.

To report a vulnerability, see
[SECURITY.md](https://github.com/The127/aibox/blob/main/SECURITY.md).

## How it works

### The VM

- On Linux the VM runs on QEMU's microvm machine type. On macOS it runs on
  Virtualization.framework.
- The project folder of `aibox run`, the mounts and the skills are shared
  into the VM with virtio-fs. Your user on the
  host is the user in the VM, so the files it makes are yours. The host
  enforces the shares that are read-only.
- The root disk is read-only for QEMU and for the VM. The init puts an
  overlay in RAM over it only to make the mount points, then makes the root
  read-only again. In the VM only `/tmp`, `/run`, `/dev/shm`, the home, the
  project and the state disk take writes.
- The kernel has no modules, no network drivers, no `bpf()` syscall, no
  ftrace and no kprobes. The kernel for amd64 has no PCI and no ACPI either.
  The kernel for arm64 has them.

### The network

- All traffic of the VM goes over vsock to the proxy on the host.
- On Linux each VM gets a vsock namespace of its own, which aibox creates in
  an unprivileged user namespace, so no root is needed. Every VM uses the
  same address, CID 3, and other processes on the host cannot connect to it.
- On macOS aibox owns the VM, and only the process that owns a VM can reach
  its vsock. So no namespace is needed there.
- The terminal is an SSH session over the same vsock.

### The host side

- On Linux QEMU runs in a bubblewrap sandbox with no network, no environment
  and no writable file system. It sees only its own program, libraries and
  firmware and a copy of the kernel. Devices, the disks and the sockets reach
  it as open files from aibox, and it filters its own syscalls.
  `aibox run --no-sandbox` runs it without bubblewrap, to debug it.
- Once QEMU runs, aibox restricts itself too. It takes no new privileges.
  Landlock rules let it read `/etc` for DNS and connect only to the ports of
  the allow list. A seccomp filter refuses ptrace, mount, namespace and
  similar syscalls. This needs Linux 6.7 or newer.
- On macOS, once the VM runs, aibox confines itself with Seatbelt, the
  sandbox of macOS. It reads the contents of no files but those of the
  resolver, though it sees the metadata of all. It connects over TCP only to
  the ports of the allow list, reaches the name service of the system,
  starts no programs and cannot read or signal other processes. The files it
  opened before, such as the logs, and the VM keep working.
- On macOS the VM runs in a process of Virtualization.framework. aibox
  cannot put that process into a sandbox of its own, as it does QEMU with
  bubblewrap, so `--no-sandbox` changes nothing on macOS.
- The VM should end with aibox, even when aibox is killed or crashes. On
  macOS the process of Virtualization.framework does, and it also serves the
  shares, so nothing is left. On Linux aibox starts QEMU and virtiofsd with a
  parent death signal, so the kernel kills them when aibox dies, and
  bubblewrap does the same for QEMU. The kernel sends that signal when the
  thread that started the program ends, so aibox starts all of them from one
  thread that lives as long as aibox does. virtiofsd forks the process that
  serves the share, which ends by a signal of its own. That this works with
  the real virtiofsd is not yet tested.
- aibox refuses to start in your home folder, above it, or above `~/.aibox`.
  It compares folders rather than paths, so a symlink, another case on a file
  system that ignores case, or a bind mount does not get past it. On a Mac
  `/users/YOU` and `/System/Volumes/Data/Users/you` are your home folder too,
  and `/System/Volumes/Data` holds it. aibox refuses those as well.
