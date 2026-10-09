# Project config

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
git:
  - remote: github.com/owner/repo
    fetch: true
    push:
      - aibox/*
```

`git` has a page of its own, [Git](git.md).

## allow

The hosts the VM may reach. The proxy refuses everything else.

- `example.com` allows port 443, `example.com:8443` another port.
- `*.example.com` matches every subdomain of `example.com`, but not
  `example.com` itself.
- `preset:NAME` stands for the hosts a tool needs, see the table below.
- A name that resolves into the host's own networks, such as loopback,
  link-local or private addresses, is refused, unless the list names that
  address itself.
- `127.0.0.1:8123` or `[::1]:8123` is a port on the loopback of your
  machine. See [Ports of your machine](#ports-of-your-machine).

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

## Ports of your machine

A server that listens on the loopback of your machine, such as a local MCP
server, is reached from the VM at the same address once you allow it:

```yaml
allow:
  - 127.0.0.1:8123
```

```
claude mcp add -t http my-server http://127.0.0.1:8123/mcp
```

- Write the address the server listens on, `127.0.0.1` or `[::1]`. The VM
  has IPv4 only, so the port is at `127.0.0.1` and `localhost` in the VM
  either way, and one port can be allowed on only one of the two.
- `localhost` is not allowed as an entry. It stands for both addresses, and
  any user on your machine could listen on the one the server does not.
  `0.0.0.0` and `::` are not allowed either, since they lead to the
  loopback too.
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

How it works: aibox tells the init of the VM the ports on its kernel command
line. The init listens on `127.0.0.1` in the VM for each port and sends each
connection to the proxy as `localhost` and the port. For `localhost` the
proxy does not ask DNS. It dials the one loopback address the allow list
names for that port. So the server sees the `Host` it expects, since many
local servers refuse other names to guard against DNS rebinding.

## memory, cpus

The memory of the VM in MiB and its number of CPUs. The flags `--memory` and
`--cpus` take precedence. Without either, the VM gets 2048 MiB and 2 CPUs.

## disk

The size in GiB of the disk that keeps `/usr/local` and `~/.cache` of the VM
between runs, 16 unless set. The disk takes up space on the host only as it
fills. Its size is fixed when the disk is created on the first run, so a
later change has no effect.

## mounts

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

## path

Folders in the VM that go in front of its `PATH`, for Claude Code and the
shell alike, for example `/opt/go/bin` from the mount above.

An entry that is a variable name, such as `PATH` or `DIRENV_PATH`, stands for
the folders in that variable of the host, less the relative and empty ones.
So the tools of a `nix develop` or `direnv` shell on the host are found in the
VM, if `/nix/store` is mounted at the same path.

## env

Variables for the command in the VM. `NAME=value` sets a value, `NAME` alone
passes the value the host has when the VM starts, which is how a secret gets
in without being written into the file. aibox refuses the variables it sets
itself: `AIBOX`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `PATH`, `TERM`, `LANG`,
the proxy variables, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` and
`GOMODCACHE`.

`ANTHROPIC_API_KEY` is the exception to passing values on. For `aibox run`
as for tasks, the key stays on your machine and the VM gets a placeholder,
see [Login](tasks.md#login).
