# aibox

[![ci](https://github.com/The127/aibox/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/The127/aibox/actions/workflows/ci.yml)

aibox runs [Claude Code](https://code.claude.com) in a small virtual machine
on your computer.

Claude Code can read and change files, run commands and access the internet.
That is what makes it useful, but it also means a bad prompt or a malicious
file in a repository can make it do things you did not want. aibox limits the
damage. Claude Code only sees the files you give it, and it can only connect
to hosts you allow.

You can use aibox in two ways:

- `aibox run` starts Claude Code in your project folder, like you would on
  your machine. Changes show up in the folder right away.
- `aibox task` hands Claude Code a job to do on its own. It works on a clone
  of your repository and you get its commits back to review. The VM is
  deleted afterwards.

aibox runs on Linux with QEMU and on macOS with Virtualization.framework. The
VM boots in about a second.

Note that with `aibox run` the VM can change anything in your project folder,
`.git` included. Treat those changes like a pull request from a stranger.
Read [What the VM writes](https://the127.github.io/aibox/what-the-vm-writes.html)
before you start.

## Installing

On macOS with Apple silicon, install it with [Homebrew](https://brew.sh):

```
brew install --cask the127/tap/aibox
```

On Linux, download a package from the
[releases](https://github.com/The127/aibox/releases). There is a `deb` and an
`rpm` for amd64, which pull in QEMU, virtiofsd and bubblewrap. There is also
a `tar.gz` with just the binary, and then you install those three yourself.
aibox needs Linux 6.7 or newer and access to `/dev/kvm`.

With [Nix](https://nixos.org), on Linux on amd64 or macOS on Apple silicon:

```
nix profile install github:The127/aibox
```

The first `aibox run` downloads the VM image, which is about 150 MB.
[Installing](https://the127.github.io/aibox/installing.html) has the details
and shows how to check a download.

## Your first run

Go to a project and start aibox:

```
cd ~/projects/my-app
aibox run
```

This boots the VM with `my-app` at `/project` and starts Claude Code in it.
Open the project in your editor next to it and watch the changes come in.

Log in to Claude Code like you would on your machine. aibox keeps the login
for the next run.

The first run also writes a config for the project. It only allows the hosts
Claude Code needs. To allow more, run:

```
aibox config edit
```

## Your first task

A task has nobody there to log in, so it takes a token from your shell
instead. First make one with `claude setup-token` and export it as
`CLAUDE_CODE_OAUTH_TOKEN`. Then add it to `env` in the project config:

```yaml
env:
  - CLAUDE_CODE_OAUTH_TOKEN
```

Now give Claude Code a job:

```
aibox task "fix the flaky test in internal/proxy"
```

When the task is done, aibox prints a command that fetches its commits into
a branch. Read them before you use them.

Note that a task can use every host, port and secret the config gives it,
with nobody watching. Read [Tasks](https://the127.github.io/aibox/tasks.html)
before you run one. It also explains
[when to use an API key](https://the127.github.io/aibox/tasks.html#login)
instead of a token.

## Documentation

The documentation is at <https://the127.github.io/aibox>. It covers tasks,
the project config, git, containers, what aibox protects you from and what
it doesn't, and how it all works.

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the
development setup and how to build the VM image. In short: commits follow
plain Conventional Commits (`type: description`, no scopes) and must be DCO
signed off (`git commit -s`). Git hooks check both.

## Security

Please report vulnerabilities privately, see [SECURITY.md](SECURITY.md).

## License

aibox is licensed under the [Apache License 2.0](LICENSE).
