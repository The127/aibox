# Security

A prompt or a file in a repository can steer Claude Code. So aibox doesn't
trust anything in the VM: not Claude Code, not the programs it runs, and not
what they write.

[Threat model](threat-model.md) says who aibox defends you against and
where the trust boundaries are. This page has the details.

To report a vulnerability, see
[SECURITY.md](https://github.com/The127/aibox/blob/main/SECURITY.md).

## What aibox protects

- The VM can't see your other files. With `aibox run` it sees your project
  folder. A task only sees a clone of a commit. Both also see the folders
  you list in `mounts`, and your skills, agents and commands from
  `~/.claude`, read-only. `aibox run` also copies your `~/.claude/CLAUDE.md`
  and a few settings that start no program and hold no secret into the VM's
  home. The project config can keep each of them out. See
  [agents, commands, claude-md, settings](config.md#agents-commands-claude-md-settings).
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
- The VM can't read your clipboard whenever it likes. It gets one image,
  and only right after you press Ctrl+V, or Alt+V on Windows. See
  [Pasting images](usage.md#pasting-images).
- The programs that run the VM are locked down. On Linux QEMU runs in a
  bubblewrap sandbox, and aibox restricts itself with Landlock and seccomp.
  On macOS aibox restricts itself with Seatbelt. See
  [The host side](#the-host-side).
- aibox only takes a downloaded image if its SHA-256 was built into its
  release. Before each start a release checks the kernel against the
  SHA-256 built into it, whether it downloaded the image or a package
  installed it. An image you pass with `--image` isn't checked, and neither
  is the image of a build from a checkout. On Linux QEMU boots the copy aibox
  checked. On macOS Virtualization.framework reads the kernel again from its
  file, so a change right after the check isn't caught. See
  [Packaging aibox](installing.md#packaging-aibox).

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
- Diffs you accept or edit in your IDE. aibox checks that a diff is for a
  file in the project, but the IDE opens the file later. In between, the VM
  can replace the file with a symlink to a file outside the project. If you
  then accept or edit the diff in the IDE, the IDE writes through the
  symlink. Approve changes in the terminal instead. See
  [In your IDE](usage.md#in-your-ide).
- Your clipboard right after Ctrl+V, or Alt+V on Windows. aibox can't
  tell which program in the VM you pressed the key in. After a Ctrl+V in
  bash or vim, the VM can read the image on your clipboard too. aibox also
  counts on your terminal not to echo text a program chose back as input,
  as current terminals don't.
  A terminal that does lets any program type into it, with or without
  aibox.
- Your browser, when it opens a server in the VM through the
  [ports](config.md#ports). Cookies don't keep ports apart, so a page from
  the VM can read and change the cookies of your other servers on
  `127.0.0.1` or `localhost`. It can also make your browser send requests
  to them. Any program or user on your machine, and any website you
  visit, can connect to the ports too.
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

The project folder of `aibox run`, the mounts, the skills, the agents and
the commands are shared into the VM with virtio-fs, or with 9p on Windows.
Your user on your machine is the user in the VM, so the files it creates
belong to you. Your machine enforces the read-only shares, not the VM.

The root disk is read-only, for QEMU and for the VM. The init puts an
overlay in RAM over it, only to create the mount points, and then makes the
root read-only again. In the VM, only `/tmp`, `/run`, `/dev/shm`, the home,
the project and the state disk can be written to.

The kernel has no modules, no network drivers, no `bpf()` syscall, no ftrace
and no kprobes. The kernel QEMU boots on amd64 has no PCI and no ACPI either.
The arm64 kernel has them. The amd64 image also carries `vmlinuz-hyperv`, a
kernel for Hyper-V on Windows, which needs both; Linux never boots it. On
Windows the shares are 9p, served by aibox itself, which enforces the
read-only ones as virtio-fs does elsewhere.

### The network

All of the VM's traffic goes over vsock to the proxy on your machine. The
terminal is an SSH session over the same vsock. On a Linux kernel without
vsock namespaces, a serial port takes the place of vsock, see below.

On Linux each VM gets its own vsock namespace. aibox creates it in an
unprivileged user namespace, so it doesn't need root. Every VM uses the same
address, CID 3, and no other process on your machine can connect to it.

Linux has vsock namespaces since 7.0. On an older kernel, or one without
vsock, the VM has no vsock at all. It reaches aibox over a virtio serial
port instead. QEMU connects the port to a socket pair, and aibox holds the
other end. No file on your machine leads to it, so no other process can
reach the VM. The proxy, the terminal and everything the proxy carries
share the port, as streams that [yamux](https://github.com/hashicorp/yamux)
multiplexes on it.

On macOS aibox owns the VM, and only the process that owns a VM can reach
its vsock. So it doesn't need a namespace.

On Windows the VM's sockets are Hyper-V sockets, and the service that makes
the VM lets only your user and the helper that serves its proxy listen
on them. Connecting into the VM stays with administrators, and nothing
does it. The helper runs in an AppContainer of its own, and the service
puts filters of the Windows Filtering Platform on that container: it
connects only to the TCP ports of the allow list, to any host on them,
and, on your machine's loopback, to the ports of the allow list and the
one aibox serves the services of the proxy on. Windows keeps an
AppContainer off the loopback, so the service lets this one on, in a
list of the whole machine that it records, and takes it off again once
aibox has ended the helper, or at its next start after a crash, or when
it is uninstalled. The filters don't cover names: Windows looks them up
for the helper outside its container, so a VM that took the helper over
could look up any name, and send data out in the names it asks for. The
helper holds no secret of aibox's to send.

The [ports](config.md#ports) go the other way, from your machine into the
VM. aibox listens on them on `127.0.0.1` and passes each connection as a
channel of the terminal's SSH session. The VM's init only connects such a
channel to its own `127.0.0.1`. aibox opens the ports before it locks
itself down. The lock-down lets it accept connections on them, but not
listen on new ones.

### The Windows service

Hyper-V serves only administrators, so on Windows a service that runs as
SYSTEM makes the VMs. It is the one part of aibox with more rights than
you, and it keeps them to itself: it boots its own copy of the kernel and
the root disk, from a folder only administrators can change, and takes no
path from you. [How it works](usage.md#how-it-works-1) says what it takes
instead. The VM gets at one file of yours, the state disk, only after the
service checked that you may write it, and only until the VM is gone: the
entry on the file is for an account of that one VM, which goes with it. A
VM may have as much memory and as many processors as the machine has,
and all VMs together no more memory than that. What the service refuses,
it says; what fails inside it goes to the Application event log, not to
you.

### The host side

On Linux QEMU runs in a bubblewrap sandbox with no network and no writable
file system. Its only environment variable is `LD_LIBRARY_PATH`, which
names the folders its libraries are in. It only sees its own program,
libraries and firmware and a copy of the kernel. Before QEMU starts, aibox
asks your system's loader which libraries QEMU loads and binds only those
files. Devices, disks and sockets come in as open files from aibox. QEMU
also filters its own syscalls. `aibox run --no-sandbox` runs it without
bubblewrap, for debugging.

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

Before the VM starts, aibox works in folders an earlier VM could write. git
reads the project's `.git` for your identity and for the bundle of a task.
With `ide: true` aibox writes the IDE's lock file into the VM's home. A
symlink the VM left there could lead a write to a file outside. So aibox
does that work in a helper, a short-lived copy of itself that the kernel
lets create, change and delete files in one folder only. On Linux that's
Landlock, on macOS Seatbelt. The kernel checks where a path really leads, so
a symlink out of the folder can't be written through, by the helper or by a
program it starts. When git reads `.git` it may write no file. For the
bundle of a task it may write only in the task's folder in `~/.aibox`. The
lock file goes into the VM's home. The helper can still read any file you
can and reach the network. On Linux it can also still change the
permissions, owner, times and extended attributes of a file outside, since
Landlock doesn't restrict those.

On Windows aibox serves the VM from two helpers, copies of itself. One
serves the shares, with your rights, as it must to read and write them,
and does nothing else. The other serves the proxy, in an AppContainer: it
parses what the VM sends through the proxy, reads none of your files,
holds no secret, has no console and reaches only the network the service
lets it. The Claude API forwarder, the git broker, the IDE bridge and the
clipboard stay in aibox, with the API key, your SSH keys and git logins
and the token of the IDE: the helper hands their requests to aibox over a
port of the loopback, which answers only requests with a token the
helper got from aibox. The terminal stays in aibox as well. aibox reads
what the helper says as it would what the VM says. Both helpers are in a
job, which Windows ends with aibox, however aibox ends. The AppContainer
has a profile under your account, which aibox removes when the helper
ends. A profile an aibox that was killed left behind goes with the next
run without `--no-sandbox`. `aibox run
--no-sandbox` runs the helper of the proxy outside its container, with
no filters.

With the [clipboard](config.md#clipboard) on, aibox starts a helper before
it locks itself down. The helper isn't locked down, because it has to start
`osascript`, `wl-paste` or `xclip`. It only reads the clipboard when aibox
asks, and ends when aibox does.

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

aibox checks the host folder of a mount the same way. It also refuses a
mount inside `~/.aibox`, because each project's config and login live there.
