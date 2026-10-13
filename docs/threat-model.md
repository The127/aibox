# Threat model

This page says who aibox defends you against, what it keeps safe, and where
its limits are. [Security](security.md) has the details of each defense.

## Who aibox defends you against

aibox defends you against anything that can steer Claude Code: a prompt, a
file in a repository, a web page Claude Code reads or a package it installs.
aibox assumes the worst. Whatever runs in the VM is under an attacker's
control, root in the VM included, and it may try bugs in the VM's kernel.

Other programs on your machine can't reach the VM either. On Linux each VM
has its own vsock namespace, so no other process can connect to it. On a
Linux kernel without vsock namespaces, the VM talks to aibox over a serial
port that only aibox holds. On macOS only aibox can reach its VM's vsock.
The exceptions are the [ports](config.md#ports) you list. Any program on
your machine can connect to them, and through them to the VM's servers.

Other programs also can't pose as a server you allowed. A server on your
machine's loopback usually listens on only one address, `127.0.0.1` or
`[::1]`. That's why the allow list takes the exact address and not
`localhost`, which means both. Otherwise another user could listen on the
other address and get the VM's connections.

## What aibox keeps safe

- Your files outside the project folder and the mounts.
- Your API key, your git login and SSH keys, and your IDE's token. Note that
  a subscription token and the secrets in `env` do go into the VM.
- Your clipboard, apart from what you paste with Ctrl+V: one image the VM
  reads right after the key, or text aibox types. Set `clipboard: false`
  in the config to keep that out too.
- Your machine's own networks and the servers that listen on its loopback.
- The rest of the internet. Data leaves only through what you allowed, like
  a host, a port or a branch.
- The branches, tags and repositories the `git` list doesn't name.
- Your machine while the VM runs. The VM can't run code on it directly. Code
  it writes into the project can run later. See [below](#what-aibox-doesnt-stop).
- Your aibox config. It decides everything else on this list.

## Why the config isn't in the project

The project config decides what the VM gets: the hosts, the ports, the
secrets, the mounts and the branches it can push to. So it lives on your
machine, in `~/.aibox/projects/<escaped path>/config.yaml`, and never in the
project or its git repository. aibox doesn't read a config from the project
at all.

People ask for a config file in the repository, like a dev container's
`devcontainer.json`. aibox can't have one, for two reasons:

- A repository would pick its own permissions. Whoever wrote it, or slipped
  a change into it, would decide which hosts and secrets the VM gets. Running
  `aibox run` in a stranger's repository would hand them your secrets.
- The VM would rewrite its own config. With `aibox run` the VM can change
  anything in the project, `.git` included. A config there could grant more
  in one session and take effect at the next start. A task's commits could
  carry one too, and you'd merge it with the rest of the change.

Your [starting config](config.md#your-own-starting-config) for new
projects, `~/.aibox/config.yaml`, is out of reach of the VM too.

aibox reads the config once, when it starts. Nothing the VM does while it
runs changes what it gets. To change the config, run `aibox config edit` on
your machine.

## Trust boundaries

Everything the VM can reach on your machine goes through one of these.

| Boundary | What crosses it | What aibox does |
|---|---|---|
| The hypervisor | The VM's CPU, memory and devices | The VM has its own kernel. On Linux it runs on QEMU in a bubblewrap sandbox. On macOS it runs on Virtualization.framework, which aibox can't sandbox. On both, aibox locks itself down once the VM runs. See [The host side](security.md#the-host-side). |
| The shares | The project, the mounts, your skills, agents and commands, and the VM's home | Your machine enforces the read-only shares. aibox won't start in your home folder or above it, and won't mount your home folder, a folder above it, or a folder in or above `~/.aibox`. See [mounts](config.md#mounts). |
| The proxy | All network traffic, over vsock or a serial port | The VM has no network card. The proxy only connects to the allow list and refuses names that lead to your own networks. See [allow](config.md#allow). |
| The git broker | git's requests | The broker adds your login on your machine, only for the repositories and branches the config names. See [Git](git.md). |
| The key forwarder | Requests to the Claude API | The VM gets a placeholder key. aibox adds the real one. See [API key](tasks.md#api-key). |
| The IDE bridge | Selections, diffs and errors | With `ide: true`, the bridge passes on only those messages, and only for files in the project. See [In your IDE](usage.md#in-your-ide). |
| The clipboard | One image or the text after Ctrl+V | aibox lets the VM read the clipboard only right after you press Ctrl+V, once, and only an image. Text it types, as a terminal pastes. Tasks never get it. See [Pasting](usage.md#pasting). |
| The terminal | What Claude Code shows you | `aibox run` passes the VM's terminal straight through. |
| The ports | Connections from your machine to servers in the VM | aibox listens only on `127.0.0.1`, only on the ports the config lists, and only with `aibox run`. The VM only connects them to its own loopback. See [ports](config.md#ports). |
| A task's results | The bundle, the logs and `result.json` | For `aibox task`, aibox checks each file and the bundle, and strips control characters before text reaches your terminal. See [How a task runs](tasks.md#how-a-task-runs). |
| Your project folder | What the VM writes | aibox doesn't check it. Treat it like a pull request from a stranger. See [What the VM writes](what-the-vm-writes.md). |

## Attacks and what stops them

| The VM tries to | What stops it |
|---|---|
| read a file outside the project | The VM only sees its shares. |
| get aibox to write a file outside through a symlink in the project or its home | aibox works in those folders in a helper that the kernel lets create, change and delete files only there. On Linux it can still change the permissions and owner of a file outside. See [The host side](security.md#the-host-side). |
| give itself more hosts, secrets or branches | The config isn't in the project, and aibox reads it once when it starts. |
| change what mise installs at the next start | With `mise: true`, aibox shows you a changed `mise.toml` or `.tool-versions` and asks before it installs from it. A task only installs from files you approved. Files they point to and mise's files on the state disk aren't covered, see below. |
| write to a read-only mount | Your machine enforces read-only, not the VM. |
| break out through a bug in its kernel | The VM has its own kernel, so a bug in it gives the attacker nothing on your machine. The kernel has no modules, no network drivers and no `bpf()`. The attacker then has to break the hypervisor too. |
| break out through a bug in the hypervisor | On Linux QEMU runs in bubblewrap with no network, only `LD_LIBRARY_PATH` in its environment and no writable file system, and filters its own syscalls. On macOS the VM runs in a Virtualization.framework process, which aibox can't put in a sandbox of its own. |
| take over aibox through a bug in the proxy or the broker | aibox locks itself down once the VM runs. On Linux 6.7 or newer that's Landlock and seccomp, on macOS Seatbelt. |
| send your data to a host of its own | The proxy only connects to the allow list. |
| reach your router or a server on your machine | The proxy refuses a name that resolves to your own networks. A loopback port needs its address on the allow list. |
| read your API key | It only has a placeholder. |
| read your git login | It never enters the VM. |
| push to `main`, delete a branch or push a tag | The broker refuses what the `git` list doesn't allow. |
| read the passwords you copy | It can only ask for the clipboard right after you press Ctrl+V, and only gets an image. Text it gets only when you paste it. |
| run code through your IDE | The IDE bridge only passes on selections, diffs and errors. |
| sneak extra branches into a task's bundle | aibox reads the bundle's header and only prints the fetch command for `aibox/task`. |
| fake a line from aibox in a task's output | aibox strips control characters and marks every line from the VM. It breaks long lines itself at your terminal's width, so a row without the mark can't hold a fake one. |
| get a changed kernel or image booted | A release checks the SHA-256 of a downloaded image and of the kernel before each start. On macOS Virtualization.framework reads the kernel again after the check, so a change right after it isn't caught. |
| keep running after aibox ends | The VM ends with aibox, even when aibox is killed. |

## What aibox doesn't stop

These are on you. [What aibox does not protect](security.md#what-aibox-does-not-protect)
has the details.

- What the VM writes into the project, `.git` included. A hook, `.envrc` or
  `package.json` can run on your machine later.
- Code the VM wrote, once you run it on your machine. That includes tests
  your IDE runs by itself, like on save, and the program you're working on
  when you build or start it. A test or a line in the program can do
  anything you can.
- What you allowed. The VM can use every host, port, secret in `env` and
  branch you give it. A mount gives it every file in that folder. A
  subscription token goes into the VM as it is, so the VM can read it.
- The servers on your loopback that you allow. They run as you, often
  without a login, and do what the VM asks. See
  [Ports on your machine](config.md#ports-on-your-machine).
- What goes through an allowed connection. `proxy.log` shows the host, not
  what was sent.
- What a task says about itself. Claude Code's last message, `result.json`
  and the authors of the commits come from the VM.
- What the VM leaves for the next `aibox run`. The VM's home folder and
  state disk stay between runs, so a steered session can leave something
  behind for the next one, like a line in `~/.claude/CLAUDE.md`, a script
  that your approved `mise.toml` runs, or a tool on the state disk.
  `aibox projects reset --logout` clears both. See
  [Starting over](usage.md#starting-over). Tasks get a new home and state
  disk each time.
- What the VM's servers send to your browser through the
  [ports](config.md#ports). A page from the VM shares the cookies of your
  other local servers on the same name, and it can make your browser send
  them requests. Any program or user on your machine, and any website you
  visit, can reach those ports too.
- What the VM shows in your terminal. With `aibox run` its output reaches
  your terminal as it is, escape sequences included.
- Force pushes to an allowed branch, and the CI a pushed branch starts.
- Diffs you accept or edit in your IDE.
- Your clipboard right after a Ctrl+V, or Alt+V on Windows, in any
  program in the VM. aibox watches for the key, not for Claude Code, so a
  Ctrl+V in bash or vim lets the VM read the image too, or types the text.
  A program in the VM can ask you to press the key and so get the text on
  your clipboard, a password you copied included. In a program that
  didn't turn bracketed paste on, such as a plain shell, each line of the
  text runs as you paste it, as in any terminal.
- Gatekeeper on macOS, for programs that come out of the VM.
- Spending. Only a task's `--timeout` is enforced by aibox.

## Tasks

`aibox task` runs in the same VM, behind the same proxy, broker and key
forwarder. What changes is what the VM gets to touch and who is watching.

| | `aibox run` | `aibox task` |
|---|---|---|
| What the VM sees | your project folder, writable | a clone of one commit |
| Who approves each step | you | nobody, because Claude Code has all permissions |
| What stays for the next run | the home and the state disk | nothing, because both are new each time |
| The clipboard | after Ctrl+V, unless `clipboard: false` | never |
| The ports | the ones the config lists | never |
| What reaches your terminal | the VM's output as it is | text with control characters stripped, each line marked |
| What reaches your project | every write, right away | only the bundle, after aibox checks it, and only once you fetch it |

So a task can't touch your working tree or `.git`, and it leaves nothing
behind in the VM. But nobody stops it from using everything the config
gives it: every host, port, secret and branch. And what it hands back is
code you haven't read. Your review is the boundary of a task. Fetch the
branch with the command aibox prints and read the whole diff before you
check it out or merge it. See
[Reviewing the commits](tasks.md#reviewing-the-commits).

## What aibox assumes

- KVM, Virtualization.framework or Hyper-V holds. aibox makes a break-out
  harder, but on macOS it can't sandbox the Virtualization.framework
  process. On Linux, `--no-sandbox` takes QEMU out of bubblewrap. On
  Windows it takes the helper that serves the proxy out of its
  AppContainer.
- Your machine isn't already taken over. A program that can change aibox,
  its config in `~/.aibox` or the programs it starts is out of scope.
- The release is the one we built. Check a download with cosign. See
  [Installing](installing.md). An image you pass with `--image`, and a build
  from a checkout, don't check the kernel.
- You read what comes out of the VM before you run it, commit it or merge
  it.
