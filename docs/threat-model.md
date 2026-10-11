# Threat model

This page says who aibox defends you against, what it keeps safe, and where
its limits are. [Security](security.md) has the details of each defense.

## Who aibox defends you against

aibox defends you against anything that can steer Claude Code: a prompt, a
file in a repository, a web page Claude Code reads or a package it installs.
aibox assumes the worst. Whatever runs in the VM is under an attacker's
control, root in the VM included, and it may try bugs in the VM's kernel.

Other programs on your machine can't reach the VM either. On Linux each VM
has its own vsock namespace, so no other process can connect to it. On macOS
only aibox can reach its VM's vsock. To allow a port on your machine's
loopback, you write its exact address, `127.0.0.1` or `[::1]`, never
`localhost`. It means both addresses, and another user could be listening
on the one your server doesn't use.

## What aibox keeps safe

- Your files outside the project folder and the mounts.
- Your API key, your git login, your SSH keys and your IDE's token. Secrets
  you put in `env`, and a subscription token, are not kept from the VM.
- Your machine's own networks and the servers that listen on its loopback.
- The rest of the internet. Data leaves only through what you allowed, like
  a host, a port or a branch.
- The branches, tags and repositories the `git` list doesn't name.
- Your machine while the VM runs. The VM can't run code on it directly. What
  it writes into the project can. See [below](#what-aibox-doesnt-stop).

## Trust boundaries

Everything the VM can reach on your machine goes through one of these.

| Boundary | What crosses it | What aibox does |
|---|---|---|
| The hypervisor | The VM's CPU, memory and devices | The VM has its own kernel. QEMU runs in a bubblewrap sandbox, and aibox locks itself down once the VM runs. See [The host side](security.md#the-host-side). |
| The shares | The project, the mounts, your skills and the VM's home | Your machine enforces the read-only shares. aibox won't start in your home folder or above it. |
| The proxy | All network traffic, over vsock | The VM has no network card. The proxy only connects to the allow list and refuses names that lead to your own networks. See [allow](config.md#allow). |
| The git broker | git's requests | The broker adds your login on your machine, only for the repositories and branches the config names. See [Git](git.md). |
| The key forwarder | Requests to the Claude API | The VM gets a placeholder key. aibox adds the real one. See [API key](tasks.md#api-key). |
| The IDE bridge | Selections, diffs and errors | With `ide: true`, the bridge passes on only those messages, and only for files in the project. See [In your IDE](usage.md#in-your-ide). |
| The terminal | What Claude Code shows you | `aibox run` passes the VM's terminal straight through. |
| A task's results | The bundle, the logs and `result.json` | For `aibox task`, aibox checks each file and the bundle, and strips control characters before text reaches your terminal. See [How a task runs](tasks.md#how-a-task-runs). |
| Your project folder | What the VM writes | aibox doesn't check it. Treat it like a pull request from a stranger. See [What the VM writes](what-the-vm-writes.md). |

## Attacks and what stops them

| The VM tries to | What stops it |
|---|---|
| read a file outside the project | The VM only sees its shares. |
| write to a read-only mount | Your machine enforces read-only, not the VM. |
| break out through a bug in its kernel | The VM's kernel isn't yours. It has no modules, no network drivers and no `bpf()`. Getting past it is only the first step. The VM then has to get past QEMU's sandbox. |
| break out through a bug in QEMU | QEMU runs in bubblewrap with no network, no environment and no writable file system, and filters its own syscalls. |
| take over aibox through a bug in the proxy or the broker | aibox locks itself down once the VM runs. On Linux 6.7 or newer that's Landlock and seccomp, on macOS Seatbelt. |
| send your data to a host of its own | The proxy only connects to the allow list. |
| reach your router or a server on your machine | The proxy refuses a name that resolves to your own networks. A loopback port needs its address on the allow list. |
| read your API key | It only has a placeholder. |
| read your git login | It never enters the VM. |
| push to `main`, delete a branch or push a tag | The broker refuses what the `git` list doesn't allow. |
| run code through your IDE | The IDE bridge only passes on selections, diffs and errors. |
| sneak extra branches into a task's bundle | aibox reads the bundle's header and only prints the fetch command for `aibox/task`. |
| fake a line from aibox in a task's output | aibox strips control characters and marks every line from the VM. |
| get a changed kernel or image booted | A release checks the SHA-256 of a downloaded image, and of the kernel before each start. On macOS Virtualization.framework reads the kernel again after the check, so a change right after it isn't caught. |
| keep running after aibox ends | The VM ends with aibox, even when aibox is killed. |

## What aibox doesn't stop

These are on you. [What aibox does not protect](security.md#what-aibox-does-not-protect)
has the details.

- What the VM writes into the project, `.git` included. A hook, `.envrc` or
  `package.json` can run on your machine later.
- What you allowed. The VM can use every host, port, secret in `env` and
  branch you give it. A subscription token goes into the VM as it is, so the
  VM can read it.
- The servers on your loopback that you allow. They run as you, often
  without a login, and do what the VM asks. See
  [Ports on your machine](config.md#ports-on-your-machine).
- What goes through an allowed connection. `proxy.log` shows the host, not
  what was sent.
- What a task says about itself. Claude Code's last message, `result.json`
  and the authors of the commits come from the VM.
- What the VM leaves for the next `aibox run`. The VM's home folder and
  state disk stay between runs, so a steered session can leave something
  behind for the next one, like a line in `~/.claude/CLAUDE.md`. Tasks get a
  new home and state disk each time.
- What the VM shows in your terminal. With `aibox run` its output reaches
  your terminal as it is, escape sequences included.
- Force pushes to an allowed branch, and the CI a pushed branch starts.
- Diffs you accept or edit in your IDE.
- Gatekeeper on macOS, for programs that come out of the VM.
- Spending. Only a task's `--timeout` is enforced by aibox.

## What aibox assumes

- KVM or Virtualization.framework holds. aibox makes a break-out harder,
  but on macOS it can't sandbox the Virtualization.framework process. On
  Linux, `--no-sandbox` takes QEMU out of bubblewrap.
- Your machine isn't already taken over. A program that can change aibox,
  its config in `~/.aibox` or the programs it starts is out of scope.
- The release is the one we built. Check a download with cosign. See
  [Installing](installing.md). An image you pass with `--image`, and a build
  from a checkout, don't check the kernel.
- You read what comes out of the VM before you run it, commit it or merge
  it.
