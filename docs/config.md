# Project config

Each project has its own config at
`~/.aibox/projects/<escaped path>/config.yaml`. It lives outside the project
on purpose, so the VM can't change it. The first `aibox run` writes it, with
comments and the hosts Claude Code needs.

To change it, run:

```
aibox config edit
```

This opens the config in the same editor git would use: `$VISUAL`, then
`$EDITOR`, then `vi`. Pass `--editor` to pick another one. When you close
the editor, aibox checks the file and tells you about mistakes.

Note that an editor that returns right away, like `code` without `--wait`,
gets checked before you've made your changes.

Here is a full example:

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

`git` has its own page, [Git](git.md).

## allow

The hosts the VM can connect to. The proxy refuses everything else.

- `example.com` allows port 443. Write `example.com:8443` for another port.
- `*.example.com` matches every subdomain of `example.com`, but not
  `example.com` itself.
- `preset:NAME` stands for the hosts a tool needs. See the table below.
- `127.0.0.1:8123` or `[::1]:8123` is a port on your machine. See
  [Ports on your machine](#ports-on-your-machine).

aibox refuses a name that resolves to an address in your machine's own
networks, like loopback, link-local or private addresses. The only way to
allow such an address is to write it into the list.

A new config only allows the hosts Claude Code needs:

| Host | Why |
|---|---|
| `api.anthropic.com` | the Claude API |
| `claude.ai` | logging in with a claude.ai account |
| `claude.com` | the sign-in page redirects through here |
| `platform.claude.com` | login tokens |
| `mcp-proxy.anthropic.com` | MCP connectors of a claude.ai account |
| `code.claude.com` | looking up documentation |

These are the presets:

| Preset | Hosts |
|---|---|
| `preset:go` | `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com`, `vuln.go.dev`, `dl.google.com`, `go.dev` |
| `preset:npm` | `registry.npmjs.org`, `registry.yarnpkg.com`, `nodejs.org` |
| `preset:pypi` | `pypi.org`, `files.pythonhosted.org` |
| `preset:cargo` | `crates.io`, `static.crates.io`, `index.crates.io`, `static.rust-lang.org` |
| `preset:github` | `github.com`, `api.github.com`, `codeload.github.com`, `*.githubusercontent.com` |
| `preset:docker` | `registry-1.docker.io`, `auth.docker.io`, `index.docker.io`, `production.cloudfront.docker.com` |

## Ports on your machine

Some servers only listen on your machine's loopback, like a local MCP
server. Once you allow the port, the VM reaches it at the same address:

```yaml
allow:
  - 127.0.0.1:8123
```

```
claude mcp add -t http my-server http://127.0.0.1:8123/mcp
```

Write the address the server listens on, `127.0.0.1` or `[::1]`. The VM only
has IPv4, so in the VM the port is at `127.0.0.1` and `localhost` either
way. That also means you can allow a port on only one of the two addresses.

You can't write `localhost`. It means both addresses, and any user on your
machine could listen on the one the server doesn't use. `0.0.0.0` and `::`
aren't allowed either, because they lead to the loopback too.

This works for any protocol over TCP, also for programs that don't use the
proxy. A program in the VM can't listen on such a port itself. Port 3128
can't be allowed, because the VM's proxy is there.

Note that the server runs as you, on your machine, outside the VM, and does
what the VM asks it to. An IDE's MCP server, for example, often has no login
and can run commands and change files outside the project. Allowing its
port lets the VM out. Only allow a port for a server you'd trust with
whatever the VM asks of it.

### How it works

aibox passes the ports to the VM's init on the kernel command line. The init
listens on `127.0.0.1` in the VM for each port. It sends each connection to
the proxy as `localhost` and the port. For `localhost` the proxy doesn't ask
DNS. It connects to the one loopback address the allow list names for that
port. That's why programs that ignore the proxy variables reach the port
too. It also means the server sees the `Host` it expects. Many local servers
refuse other names, to guard against DNS rebinding.

## memory, cpus

The VM's memory in MiB and its number of CPUs. Without them, the VM gets
2048 MiB and 2 CPUs. The flags `--memory` and `--cpus` override both.

## disk

The size in GiB of the disk that keeps the VM's `/usr/local` and `~/.cache`
between runs. The default is 16. The disk only takes up space on your
machine as it fills.

Note that the size is fixed when the first run creates the disk. Changing it
later has no effect.

## mounts

Folders on your machine that the VM can read, written as `host:guest`.
`guest` is the path in the VM. A host path starting with `~/` starts in your
home folder.

- The guest path has to stay clear of the folders the VM needs: `/project`,
  `/dev`, `/proc`, `/sys`, `/run`, `/tmp`, `/etc`, `/bin`, `/sbin`, `/lib`,
  `/lib64`, `/usr` and `/root`. It also can't overlap with other mounts.
- A mount inside `/home/user` is fine. The VM's home is a folder aibox
  manages, not your home folder. Leave `/home/user/.claude` itself alone,
  because the VM's login lives there.
- The VM caches names and attributes for the whole run. If you change the
  folder on your machine, a running VM may not see it.
- On macOS the VM runs arm64 Linux, so Mac programs don't run in it.

Your skills in `~/.claude/skills` are always mounted read-only, at the same
place in the VM's home, so Claude Code finds them. A symlink in there that
points outside the folder doesn't work in the VM. Your settings, plugins and
MCP servers are not shared. The VM starts with its own.

## path

Folders in the VM to put in front of its `PATH`, for Claude Code and the
shell. For example `/opt/go/bin` from the mount above.

An entry can also be the name of a variable, like `PATH` or `DIRENV_PATH`.
It then stands for the folders in that variable on your machine, without the
relative and empty ones. That way the VM finds the tools of a `nix develop`
or `direnv` shell, as long as `/nix/store` is mounted at the same path.

## env

Environment variables for the command in the VM. `NAME=value` sets a value.
`NAME` alone passes on the value your shell has when the VM starts. That's
how you get a secret in without writing it into the file.

aibox refuses the variables it sets itself: `AIBOX`, `HOME`, `USER`,
`LOGNAME`, `SHELL`, `PATH`, `TERM`, `LANG`, the proxy variables,
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` and `GOMODCACHE`.

`ANTHROPIC_API_KEY` is the exception. Its value is never passed on. With
`aibox run` and with tasks, the key stays on your machine and the VM gets a
placeholder. See [API key](tasks.md#api-key).
