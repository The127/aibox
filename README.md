# aibox

[![ci](https://github.com/The127/aibox/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/The127/aibox/actions/workflows/ci.yml)

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

## What the VM writes

The project folder is shared writable on purpose. So whatever the VM writes
into the project is untrusted until you have read it, like a pull request
from a stranger. That includes files that tools on the host run without
asking. lefthook runs the commands of `lefthook.yml`, direnv runs `.envrc`,
just reads the `justfile`, npm runs the scripts of `package.json`, and an IDE
starts the run configurations under `.idea/`, all from the working tree.

This includes `.git`. A key in `.git/config`, such as `core.fsmonitor` or
`core.hooksPath`, or a hook in `.git/hooks` runs the next time you use git on
the host, and neither `git status` nor `git diff` shows it. aibox does not
protect them. A read-only mount in the VM does not hold: git and IDEs
replace `.git/config` whenever they write it, for example on `git push -u`,
and the mount goes with the old file.

So:

- Trust a project in your IDE, which GoLand asks about when you first open
  it, only when the project is your own.
- Read the diff of what the VM changed before you run a build, a test, a
  script or a run configuration of the project on the host, and before you
  commit with hooks the project defines.
- Check `.git/config` and `.git/hooks` for changes you did not make.

## Installing

On macOS with Apple silicon, with [Homebrew](https://brew.sh):

```
brew install --cask the127/tap/aibox
```

On Linux, each [release](https://github.com/The127/aibox/releases) has a
`deb` and an `rpm` for amd64, which depend on QEMU, virtiofsd and bubblewrap,
and a `tar.gz` with the binary. aibox needs Linux 6.7 or newer and virtiofsd
at `/usr/libexec/virtiofsd`, as Debian 13, Ubuntu 24.04 and Fedora have it.
It also needs access to `/dev/kvm`.

The packages do not hold the VM image. The first `aibox run` downloads the
image of its release, about 150 MB, and keeps it in
`~/.aibox/image/<version>`. It takes only the image whose SHA-256 the release
built into it. After an upgrade it downloads the new image and removes the
old one.

`checksums.txt` lists the checksums of all files of a release, and cosign
signed it without a key. To check a download:

```
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/The127/aibox/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
```

## Usage

`aibox run` in a project folder boots the VM and starts Claude Code. It
refuses to start in your home folder or above it, since the VM would then
see all of your files.

| Flag | Meaning |
|---|---|
| `--shell` | Open a shell in the VM instead of Claude Code. |
| `--memory` | Memory of the VM in MiB, 2048 unless the config says otherwise. |
| `--cpus` | Number of CPUs of the VM, 2 unless the config says otherwise. |
| `--image` | A folder with `vmlinuz` and `os.ext4` to boot instead of the image of the release. |
| `--no-sandbox` | Run QEMU without its bubblewrap sandbox, to debug it. Linux only. |

The VM keeps a home folder and a disk for each project, so the login of
Claude Code, `~/.claude`, and tools installed into `/usr/local` survive
restarts. The messages of the kernel and the init go into `console.log`, and
the hosts the proxy refused into `proxy.log`, both in the folder of the
project below `~/.aibox/projects`.

## Project config

Each project has its settings in `~/.aibox/projects/<escaped path>/config.yaml`,
outside the project, so the VM cannot change them. The first `aibox run`
writes the file with comments and the hosts Claude Code needs.

```
aibox config edit
```

opens it in the editor git would use, `$VISUAL`, then `$EDITOR`, then `vi`,
or in the one given with `--editor`. Afterwards it checks the file and
reports a mistake. An editor that returns at once, such as `code` without
`--wait`, is checked before you have edited.

A full example:

```yaml
allow:
  - api.anthropic.com
  - claude.ai
  - preset:go
  - preset:github
  - registry.example.com:5000
  - "*.example.org"
memory: 4096
cpus: 4
disk: 32
mounts:
  - ~/sdk/go1.26.8:/opt/go
path:
  - /opt/go/bin
env:
  - GOFLAGS=-mod=mod
  - GITHUB_TOKEN
```

### allow

The hosts the VM may reach. The proxy refuses everything else.

- `example.com` allows port 443, `example.com:8443` another port.
- `*.example.com` matches every subdomain of `example.com`, but not
  `example.com` itself.
- `preset:NAME` stands for the hosts a tool needs, see the table below.
- A name that resolves into the host's own networks, such as loopback,
  link-local or private addresses, is refused, unless the list names that
  address itself.
- `127.0.0.1:8123`, `[::1]:8123` or `localhost:8123` for both is a port on
  the loopback of your machine. See
  [Ports of your machine](#ports-of-your-machine).

A new config allows the hosts Claude Code needs, and nothing else:

| Host | Why |
|---|---|
| `api.anthropic.com` | the Claude API |
| `claude.ai` | login with a claude.ai account |
| `claude.com` | the sign-in page redirects through here |
| `platform.claude.com` | login tokens |
| `mcp-proxy.anthropic.com` | MCP connectors of a claude.ai account |
| `code.claude.com` | documentation lookups |

The presets:

| Preset | Hosts |
|---|---|
| `preset:go` | `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com`, `vuln.go.dev`, `dl.google.com`, `go.dev` |
| `preset:npm` | `registry.npmjs.org`, `registry.yarnpkg.com`, `nodejs.org` |
| `preset:pypi` | `pypi.org`, `files.pythonhosted.org` |
| `preset:cargo` | `crates.io`, `static.crates.io`, `index.crates.io`, `static.rust-lang.org` |
| `preset:github` | `github.com`, `api.github.com`, `codeload.github.com`, `*.githubusercontent.com` |
| `preset:docker` | `registry-1.docker.io`, `auth.docker.io`, `index.docker.io`, `production.cloudfront.docker.com` |

### Ports of your machine

A server that listens on the loopback of your machine, such as a local MCP
server, is reached from the VM at the same address once you allow it:

```yaml
allow:
  - 127.0.0.1:8123
```

```
claude mcp add -t http my-server http://127.0.0.1:8123/mcp
```

- The VM has IPv4 only. Each such port is at `127.0.0.1` and `localhost` in
  the VM, and leads to the addresses the entries name for it on your
  machine: `127.0.0.1`, `::1`, or both for `localhost`.
- It works for any protocol over TCP, also for programs that do not use the
  proxy.
- A program in the VM cannot listen on such a port itself.
- Port 3128 cannot be allowed on the loopback, because the VM has its proxy
  there.

Such a server runs as you on your machine, outside the VM, and does what
the VM asks of it. An MCP server of an IDE, for example, often has no login
and can run commands and change files outside the project, so allowing its
port lets the VM out. Allow a port only for a server you would let do what
the VM asks.

### memory, cpus

The memory of the VM in MiB and its number of CPUs. The flags `--memory` and
`--cpus` take precedence. Without either, the VM gets 2048 MiB and 2 CPUs.

### disk

The size in GiB of the disk that keeps `/usr/local` and `~/.cache` of the VM
between runs, 16 unless set. The disk takes up space on the host only as it
fills. Its size is fixed when the disk is created on the first run, so a
later change has no effect.

### mounts

Folders of the host the VM sees read-only, written `host:guest`, with `guest`
the path in the VM. `~/` at the start of the host path stands for your home
folder.

- The guest path must stay clear of the folders the VM needs: `/project`,
  `/dev`, `/proc`, `/sys`, `/run`, `/tmp`, `/etc`, `/bin`, `/sbin`, `/lib`,
  `/lib64`, `/usr` and `/root`, and of the other mounts.
- A mount inside `/home/user` is fine, because the home of the VM is a folder
  of aibox, not your home on the host. Leave `/home/user/.claude` itself
  alone, the login of the VM lives there.
- Names and attributes are cached for the whole run, so a change to the
  folder on the host may not show in a running VM.
- On macOS the VM runs arm64 Linux, so programs of the Mac do not run in it.

Your skills in `~/.claude/skills` are mounted read-only at the same place in
the home of the VM, so Claude Code finds them. A symlink in there that points
outside the folder does not resolve in the VM. Your settings, plugins and MCP
servers are not shared, the VM starts with its own.

### path

Folders in the VM that go in front of its `PATH`, for Claude Code and the
shell alike, for example `/opt/go/bin` from the mount above.

An entry that is a variable name, such as `PATH` or `DIRENV_PATH`, stands for
the folders in that variable of the host, less the relative and empty ones.
So the tools of a `nix develop` or `direnv` shell on the host are found in the
VM, if `/nix/store` is mounted at the same path.

### env

Variables for the command in the VM. `NAME=value` sets a value, `NAME` alone
passes the value the host has when the VM starts, which is how a secret gets
in without being written into the file. aibox refuses the variables it sets
itself: `AIBOX`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `PATH`, `TERM`, `LANG`,
the proxy variables, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` and
`GOMODCACHE`.

## Documentation

[docs/design.md](docs/design.md) describes how aibox works, on Linux and on
macOS: the VM, the proxy, the terminal, what each project keeps, and how the
host side is sandboxed.

## Contributing

Contributions are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md), which also
has the development setup and how to build the VM image. In short: commits
follow plain Conventional Commits (`type: description`, no scopes) and must be
DCO signed off (`git commit -s`). Both rules are enforced by git hooks.

## Security

Please report vulnerabilities privately, see [SECURITY.md](SECURITY.md).

## License

aibox is licensed under the [Apache License 2.0](LICENSE).
