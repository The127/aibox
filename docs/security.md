# Security

A prompt or a file in a repository can steer Claude Code. So aibox doesn't
trust anything in the VM: not Claude Code, not the programs it runs, and not
what they write.

To report a vulnerability, see
[SECURITY.md](https://github.com/The127/aibox/blob/main/SECURITY.md).

## What aibox protects

- The VM can't see your other files. With `aibox run` it sees your project
  folder. A task only sees a clone of a commit. Both also see the folders
  you list in `mounts` and your skills in `~/.claude/skills`, read-only.
  aibox won't start in your home folder or above it.
- The VM can only reach the hosts on the allow list. It has no network card,
  only a proxy on your machine. aibox refuses a name that resolves to your
  machine's own networks, unless the list names that address itself. A port
  on your machine's loopback is only reachable when the list names it. See
  [allow](config.md#allow).
- Your API key stays on your machine. The VM gets a placeholder, and aibox
  adds the real key to the requests that go to the Claude API. See
  [API key](tasks.md#api-key).
- Your git login stays on your machine too. A broker adds it to git's
  requests from the VM, but only for the repositories and branches the
  config names. See [Git](git.md).
- The programs that run the VM are locked down. On Linux QEMU runs in a
  bubblewrap sandbox, and aibox restricts itself with Landlock and seccomp.
  On macOS aibox restricts itself with Seatbelt. See
  [The host side](#the-host-side).
- aibox only boots the image whose SHA-256 was built into its release.

## What aibox does not protect

- What the VM writes into the project. That includes `.git`, and files that
  tools on your machine run without asking. Read
  [What the VM writes](what-the-vm-writes.md).
- What you allow. The VM can reach every host on the allow list and every
  port of your machine the list names. It can use every secret in `env` and
  push to every branch the `git` list names. A subscription token goes into
  the VM as it is, so the VM can read it. A task can use all of this, with
  nobody there to stop it. See [Tasks](tasks.md).
- What goes through an allowed connection. `proxy.log` shows the host and
  port the VM asked for. It doesn't show the address behind the host, or
  what was sent. A push to `github.com` and a fetch from it look the same.
- What a task says about itself. Claude Code's last message, `result.json`
  and the authors of the commits all come from the VM.
- Force pushes and CI. The git broker can't tell a force push to an allowed
  branch from a normal push. A pushed branch can start the repository's CI,
  with its secrets. See
  [What the broker can't stop](git.md#what-the-broker-cant-stop).
- Spending. The only limit on time or spending that aibox enforces itself
  is a task's `--timeout`. Give an API key a spend limit in the Console.
- Gatekeeper on macOS. The VM can set and remove extended attributes on
  files in the project, including `com.apple.quarantine`, and files it
  creates have none. So Gatekeeper never checks a program that comes out of
  the VM. Virtualization.framework has no option to prevent that. Like
  everything the VM writes, such a program is untrusted until you've read
  it.

## How it works

### The VM

On Linux the VM runs on QEMU's microvm machine type. On macOS it runs on
Virtualization.framework.

The project folder of `aibox run`, the mounts and the skills are shared into
the VM with virtio-fs. Your user on your machine is the user in the VM, so
the files it creates belong to you. Your machine enforces the read-only
shares, not the VM.

The root disk is read-only, for QEMU and for the VM. The init puts an
overlay in RAM over it, only to create the mount points, and then makes the
root read-only again. In the VM, only `/tmp`, `/run`, `/dev/shm`, the home,
the project and the state disk can be written to.

The kernel has no modules, no network drivers, no `bpf()` syscall, no ftrace
and no kprobes. The kernel QEMU boots on amd64 has no PCI and no ACPI either.
The arm64 kernel has them. The amd64 image also carries `vmlinuz-hyperv`, a
kernel for Hyper-V on Windows, which needs both; Linux never boots it.

### The network

All of the VM's traffic goes over vsock to the proxy on your machine. The
terminal is an SSH session over the same vsock.

On Linux each VM gets its own vsock namespace. aibox creates it in an
unprivileged user namespace, so it doesn't need root. Every VM uses the same
address, CID 3, and no other process on your machine can connect to it.

On macOS aibox owns the VM, and only the process that owns a VM can reach
its vsock. So it doesn't need a namespace.

### The host side

On Linux QEMU runs in a bubblewrap sandbox with no network, no environment
and no writable file system. It only sees its own program, libraries and
firmware and a copy of the kernel. Devices, disks and sockets come in as
open files from aibox. QEMU also filters its own syscalls.
`aibox run --no-sandbox` runs it without bubblewrap, for debugging.

Once QEMU runs, aibox locks itself down too. It gives up the right to gain
new privileges. Landlock rules only let it read `/etc` for DNS and connect
to the ports on the allow list. A seccomp filter blocks ptrace, mount,
namespace and similar syscalls. This needs Linux 6.7 or newer.

On macOS aibox locks itself down with Seatbelt, the macOS sandbox, once the
VM runs. From then on it reads the contents of no files except the
resolver's, though it can still see the metadata of all files. It only
connects over TCP to the ports on the allow list, and to the system's name
service. It can't start programs or read or signal other processes. Files it
opened before, like the logs, and the VM keep working.

On macOS the VM runs in a Virtualization.framework process. aibox can't put
that process in a sandbox of its own, like it does with QEMU and bubblewrap.
That's why `--no-sandbox` changes nothing on macOS.

The VM ends with aibox, even when aibox is killed or crashes. On
macOS the Virtualization.framework process does, and it also serves the
shares, so nothing is left behind. On Linux aibox starts QEMU and virtiofsd
with a parent death signal, so the kernel kills them when aibox dies.
bubblewrap does the same for QEMU. The kernel sends that signal when the
thread that started the program ends, so aibox starts all of them from one
thread that lives as long as aibox. virtiofsd forks the process that serves
the share, and that process ends by a signal of its own.

aibox won't start in your home folder, above it, or above `~/.aibox`. It
compares folders rather than paths, so a symlink, a different spelling on a
file system that ignores case, or a bind mount doesn't get around it. On a
Mac, `/users/YOU` and `/System/Volumes/Data/Users/you` are your home folder
too, and `/System/Volumes/Data` contains it. aibox refuses those as well.
