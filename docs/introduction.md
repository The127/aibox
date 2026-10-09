# Introduction

aibox runs [Claude Code](https://code.claude.com) in a small virtual machine
on your computer.

Claude Code can read and change files, run commands and access the internet.
That is what makes it useful, but it also means a bad prompt or a malicious
file in a repository can make it do things you did not want. aibox limits the
damage. Claude Code only sees the files you give it, and it can only connect
to hosts you allow.

You can use aibox in two ways:

- `aibox run` starts Claude Code in your project folder, like you would on
  your machine. Changes show up in the folder right away. See
  [Running Claude Code](usage.md).
- `aibox task` hands Claude Code a job to do on its own. It works on a clone
  of your repository and you get its commits back to review. The VM is
  deleted afterwards. See [Tasks](tasks.md).

You can also mount other folders into the VM, read-only.

aibox runs on Linux with QEMU and on macOS with Virtualization.framework. The
VM boots in about a second.

## Why a VM

There are other ways to fence in Claude Code. Here is why aibox uses a VM
instead.

Claude Code has a [sandbox](https://code.claude.com/docs/en/sandboxing) of
its own. It's quick to turn on and good for cutting down on permission
prompts. But it only covers shell commands. Claude Code's file tools, MCP
servers and hooks still run directly on your machine. Anthropic's
[sandbox runtime](https://github.com/anthropics/sandbox-runtime) wraps the
whole process, but it still shares your kernel, and you configure every
path and host yourself.

A Docker container, like Anthropic's
[dev container](https://code.claude.com/docs/en/devcontainer), puts the
whole process in a box. But containers share the kernel with your machine,
so a kernel bug is a way out. The dev container's firewall runs inside the
same container as Claude Code, and it allows the IP addresses it looked up
when the container started. On a Mac, Docker runs all containers in one
shared Linux VM.

A VM has its own kernel. To get out, something has to break the hypervisor,
not just the kernel. aibox keeps that VM small: no network card, no kernel
modules, and a proxy on your machine as its only way to the network. It also locks
down the programs that run the VM on your machine.

Apple's [container](https://github.com/apple/container) tool also runs each
container in its own VM. But its runtime can't be locked down, and it talks
to the VM over a socket other programs of yours can reach. So on macOS aibox
runs the VM with Virtualization.framework itself, like it runs QEMU on
Linux.

[Docker Sandboxes](https://docs.docker.com/ai/sandboxes/) is the closest to
aibox. It also runs agents in a microVM, with a proxy that only lets
allowed hosts through and keeps API keys on your machine. You need to sign
in to Docker to use it. aibox is open source and needs no account. It also
has a [git broker](git.md) that only lets pushes through to the branches you
name.

## What aibox does not do

aibox keeps Claude Code away from the rest of your machine. It does not check
what Claude Code does with the things you give it. With `aibox run` the VM can
change anything in your project folder, `.git` included, and a task can use
every host and secret you allow it. Read
[What the VM writes](what-the-vm-writes.md) and [Security](security.md) to
see where the limits are.
