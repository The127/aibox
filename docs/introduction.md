# Introduction

aibox runs [Claude Code](https://code.claude.com) inside a microVM, with your
project folder shared into it. Claude Code works on the project as it would
on your machine, but it sees no other files of yours, and it reaches the
network only through a proxy that lets through the hosts you allow.

It runs on Linux with QEMU and on macOS with Virtualization.framework. The VM
boots in about a second.

```
cd ~/projects/my-app
aibox run
```

This boots the VM with `my-app` at `/project` and starts Claude Code in it.
Open the project in your editor next to it and watch the changes come in.

`aibox task` runs Claude Code unattended on the last commit of the project,
in a VM that is thrown away afterwards, and hands back its commits for you
to review.

The project folder is shared writable, so what the VM writes into it is
untrusted until you have read it. [What the VM writes](what-the-vm-writes.md)
says what that means, and [Security](security.md) what aibox protects and
what it does not.
