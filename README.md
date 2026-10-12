# aibox

[![ci](https://github.com/The127/aibox/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/The127/aibox/actions/workflows/ci.yml)

Let [Claude Code](https://code.claude.com) work on your project without
giving it your whole computer.

aibox runs Claude Code in a small virtual machine that only sees the folders
you share and only reaches the hosts you allow. The VM has its own kernel and
no network card. Its only way to the network is a proxy on your machine that
checks every connection. It boots in about a second.

![aibox run lets Claude Code change the project folder from a VM, then aibox task does a job on its own and hands back a commit](docs/demo.gif)

Claude Code can read and change files, run commands and access the
internet. That's what makes it useful, but a bad prompt or a malicious file
in a repository can turn it against you. In aibox, Claude Code can't see
your SSH keys, your cloud logins or your other projects, unless you share
them. And it can't send anything to a host you didn't allow.

## What stays the same

You start Claude Code with `aibox run` in your project folder, and it works
like it always does. Its changes show up in the folder right away, so you
keep your editor open next to it. You log in with your Claude account like
you would on your machine, and aibox keeps the login.

You can bring your own tools, install more in the VM and run containers
with podman. If you turn it on, Claude Code also connects to VS Code or
GoLand.

## Your keys and logins

Your API key never enters the VM. Claude Code gets a placeholder, and aibox
puts in the real key on the way to the Claude API. A subscription token is
different. It goes into the VM as it is, because Anthropic's terms don't
allow anyone but you to handle it.

Your git login stays on your machine too. If you list repositories in the
config, git in the VM can fetch from them and push to the branches you name.
aibox refuses pushes to other branches, deletes and tags. It can't stop a
force push to a branch you allowed.

The config lives on your machine and never in the project. So a repository
you clone can't give itself hosts, secrets or branches, and the VM can't
change its own config.

## Two ways to use it

- `aibox run` starts Claude Code in your project folder, and you work with
  it like you always do.
- `aibox task` hands Claude Code a job to do on its own. It works on a clone
  of the last commit, or of any branch, tag or commit you name, and you get
  its commits back to review. The VM is deleted afterwards. You can give it
  a plan from a file, a timeout and a budget, and run several tasks at
  once.

## Why a VM

Claude Code's own sandbox only covers shell commands. Its file tools, MCP
servers and hooks still run on your machine. A Docker container covers the
whole process, but shares your kernel. A VM has its own kernel, so a kernel
bug alone is not enough to get out.
[Why a VM](https://the127.github.io/aibox/introduction.html#why-a-vm)
compares aibox with these, Apple's container tool and Docker Sandboxes.

The [threat model](https://the127.github.io/aibox/threat-model.html) says
who aibox defends you against and where its limits are.

## Installing

aibox runs on macOS with Apple silicon, on Linux on amd64 and on Windows 11
Pro, Enterprise and Education on amd64.

On macOS and Linux, install it with [Homebrew](https://brew.sh):

```
brew install --cask the127/tap/aibox
```

On Linux, aibox needs Linux 7.0 or newer with vsock, and access to
`/dev/kvm`. Homebrew installs just the binary, so also install QEMU,
virtiofsd and bubblewrap from your distribution. The `deb` and `rpm` on the
[releases](https://github.com/The127/aibox/releases) page pull them in for
you.

On Windows, download `aibox.exe` from the releases and, as administrator,
run `aibox service install` once. See
[Installing](https://the127.github.io/aibox/installing.html#windows).

With [Nix](https://nixos.org), on Linux on amd64 or macOS on Apple silicon:

```
nix profile install github:The127/aibox
```

[Installing](https://the127.github.io/aibox/installing.html) has the details
and shows how to check a download.

## Your first run

Go to a project, let aibox suggest the hosts its tools need, and start it:

```
cd ~/projects/my-app
aibox config suggest
aibox run
```

The VM only reaches the hosts you allow. `aibox config suggest` looks at the
files in your project, like `go.mod` or `package.json`, and offers presets,
the hosts a tool like Go or npm needs. It shows them and asks before it adds
them.

`aibox run` boots the VM with `my-app` at `/project` and starts Claude Code
in it. The first run downloads the VM image, which is about 150 MB, unless
your package ships it. Log in to Claude Code like you would on your machine.

You log in once for each project, because each project has a VM home of its
own. aibox doesn't copy the login from one project to another. That would
mean aibox handles your login, and Anthropic's terms don't allow anyone but
you to do that.

When a tool still can't reach a host, the error names it, and the preset
that allows it if there is one. To allow more, run:

```
aibox config edit
```

`proxy.log` in the project's folder in `~/.aibox/projects` shows each host
the proxy connected to or refused.

Note that the VM can change anything in your project folder, `.git`
included. Treat those changes like a pull request from a stranger.
[What the VM writes](https://the127.github.io/aibox/what-the-vm-writes.html)
explains why and what to look out for.

## Your first task

A task works on a clone of the last commit, with nobody there to log in. So
it takes a token from your shell instead. First make one with
`claude setup-token` and export it as `CLAUDE_CODE_OAUTH_TOKEN`. Then add it
to `env` in the project config:

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
with nobody watching. [Tasks](https://the127.github.io/aibox/tasks.html)
explains what that means for your config, and
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
