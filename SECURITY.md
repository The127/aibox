# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately via
[GitHub private vulnerability reporting](https://github.com/The127/aibox/security/advisories/new).
Do not open a public issue for security problems.

You can expect a timely response. Please include enough detail to reproduce
the issue (affected component, platform, setup, steps).

There is no bug bounty program.

## Supported versions

Only the latest release and the `main` branch get security fixes.

## Scope notes

aibox runs Claude Code, which a prompt or the content of a repository can
steer, inside a VM. Everything inside the VM is untrusted. The project folder
is shared writable on purpose, so what the VM writes into it is untrusted
too, until you have read it. The README says what that means in
[What the VM writes](README.md#what-the-vm-writes). A change the VM makes to
the project, `.git` included, is not a vulnerability by itself.

Of particular interest are issues in:

- the boundary between the VM and the host: the VM reaching files of the host
  outside the project folder, writing to a share that is read-only, or
  reaching the host other than through the proxy and the terminal,
- the proxy: a host off the allow list reached, or the host's own networks
  reached through an allowed name,
- the confinement of the host side: QEMU in its bubblewrap sandbox, and aibox
  under Landlock and seccomp on Linux or Seatbelt on macOS,
- the image download: aibox taking an image other than the one its release
  was built with.
