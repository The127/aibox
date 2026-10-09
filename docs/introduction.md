# Introduction

aibox runs [Claude Code](https://code.claude.com) in a microVM on your
machine. In the VM, Claude Code has the files you give it and no others of
yours. It reaches the network through a proxy that lets through only the
hosts you allow.

- `aibox run` shares the project folder into the VM, writable, and starts
  Claude Code in it, to work with you.
- `aibox task` gives the VM a clone of a commit instead of the folder, runs
  Claude Code in it unattended, and hands back its commits for you to
  review. The VM is thrown away afterwards.

Both can also see folders you mount for them, read-only.

It runs on Linux with QEMU and on macOS with Virtualization.framework. The VM
boots in about a second.

```
cd ~/projects/my-app
aibox run
```

This boots the VM with `my-app` at `/project` and starts Claude Code in it.
Open the project in your editor next to it and watch the changes come in.

With `aibox run` the project folder is shared writable, so what the VM
writes into it is untrusted until you have read it.
[What the VM writes](what-the-vm-writes.md) says what that means, and
[Security](security.md) what aibox protects and what it does not.
