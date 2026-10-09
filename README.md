# aibox

[![ci](https://github.com/The127/aibox/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/The127/aibox/actions/workflows/ci.yml)

aibox runs [Claude Code](https://code.claude.com) inside a microVM, with your
project folder shared into it. Claude Code works on the project as it would
on your machine, but it sees no other files of yours, and it reaches the
network only through a proxy that lets through the hosts you allow.

It runs on Linux with QEMU and on macOS with Virtualization.framework. The VM
boots in about a second.

The project folder is shared writable, so treat what the VM writes into it,
`.git` included, like a pull request from a stranger. Read
[What the VM writes](https://the127.github.io/aibox/what-the-vm-writes.html)
before you use aibox.

## Installing

On macOS with Apple silicon, with [Homebrew](https://brew.sh):

```
brew install --cask the127/tap/aibox
```

On Linux, each [release](https://github.com/The127/aibox/releases) has a
`deb` and an `rpm` for amd64, which depend on QEMU, virtiofsd and bubblewrap,
and a `tar.gz` with the binary. aibox needs Linux 6.7 or newer and access to
`/dev/kvm`.

With [Nix](https://nixos.org), on Linux on amd64 or macOS on Apple silicon:

```
nix profile install github:The127/aibox
```

The first `aibox run` downloads the VM image, about 150 MB.
[Installing](https://the127.github.io/aibox/installing.html) has the details
and how to check a download.

## A first run

```
cd ~/projects/my-app
aibox run
```

This boots the VM with `my-app` at `/project` and starts Claude Code in it.
Open the project in your editor next to it and watch the changes come in.

Log in to Claude Code in the VM as you would on your machine. The login is
kept for the next run. The first run also writes the project config, which
allows the hosts Claude Code needs and nothing else. Add hosts with:

```
aibox config edit
```

## A first task

A task runs Claude Code unattended on the last commit, in a VM that is thrown
away afterwards. Nobody is there to log in, so it takes a token from your
shell. Make one with `claude setup-token`, set it as
`CLAUDE_CODE_OAUTH_TOKEN`, and pass it on in the project config:

```yaml
env:
  - CLAUDE_CODE_OAUTH_TOKEN
```

[Login](https://the127.github.io/aibox/tasks.html#login) says when to use an
API key instead.

```
aibox task "fix the flaky test in internal/proxy"
```

At the end aibox prints the command that fetches the commits of the task
into a branch. Read them before you use them. A task can use every host,
port and secret the config gives it, so read
[Tasks](https://the127.github.io/aibox/tasks.html) before you run one.

## Documentation

The documentation is at <https://the127.github.io/aibox>. It describes
tasks, the project config, git, what the VM writes, what aibox protects
against and how aibox works.

## Contributing

Contributions are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md), which also
has the development setup and how to build the VM image. In short: commits
follow plain Conventional Commits (`type: description`, no scopes) and must be
DCO signed off (`git commit -s`). Both rules are enforced by git hooks.

## Security

Please report vulnerabilities privately, see [SECURITY.md](SECURITY.md).

## License

aibox is licensed under the [Apache License 2.0](LICENSE).
