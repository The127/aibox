# Project config

Each project has its own config at
`~/.aibox/projects/<escaped path>/config.yaml`. It lives outside the project
on purpose, so the VM can't change it. The first `aibox run` writes it, with
comments and the hosts Claude Code needs, or from
[your own starting config](#your-own-starting-config).

To change it, run:

```
aibox config edit
```

This opens the config in the same editor git would use: `$VISUAL`, then
`$EDITOR`, then `vi`, or Notepad on Windows. Pass `--editor` to pick
another one. When you close the editor, aibox checks the file and tells
you about mistakes.

An editor that returns right away, like `code` without `--wait`,
gets checked before you've made your changes.

## Your own starting config

If you use the same hosts, mounts or memory in most projects, you don't
have to add them to each one. Write them into a template once:

```
aibox config edit --template
```

This opens `~/.aibox/config.yaml`, which starts as a copy of the default
config. From then on, every new project gets a copy of it, comments
included, instead of the default. Projects that have a config already keep
it, and a change to the template doesn't reach them. So each project's
config still says everything its VM gets.

If the template has a mistake, aibox says so and starts no new project from
it.

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
skills: all
settings: none
ide: true
clipboard: false
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

When the proxy refuses a host that a preset allows, its message names the
preset.

### Suggesting presets

A new config doesn't know which tools your project uses. To find out, run
this in the project folder:

```
aibox config suggest
```

aibox looks at the names of the files in the folder and suggests the
tools they point to, with the files that show each one and the hosts of
its preset. It asks for each tool before it adds its preset to `allow`. Start aibox again for them to apply. Without a
terminal to ask, or when `allow` isn't a list with one entry per line, it
only prints the lines to add with `aibox config edit`.

| Preset | Files |
|---|---|
| `preset:go` | `go.mod`, `go.work` |
| `preset:npm` | `package.json`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml` |
| `preset:pypi` | `pyproject.toml`, `requirements.txt`, `setup.py`, `setup.cfg`, `Pipfile`, `uv.lock`, `poetry.lock` |
| `preset:cargo` | `Cargo.toml`, `Cargo.lock` |

A project may come from a stranger, so it must not pick its own
permissions. That's why aibox only reads the names of plain files, and only
in the top folder, and never what's in them. It only ever suggests these
presets, never a host, a port, a mount or a variable from the project.
`preset:github` and `preset:docker` are never suggested. A git remote or a
`Dockerfile` doesn't mean the VM needs them.

Note that anyone can upload files to package registries, so a program in
the VM can send data there too. Only add a preset the project needs.

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
can't be allowed, because the VM's proxy is there. With [`ide`](#ide)
turned on, 3131 can't be allowed either, because Claude Code reaches your
IDE there.

Keep in mind that the server runs as you, on your machine, outside the VM,
and does what the VM asks it to. An IDE's MCP server, for example, often has
no login and can run commands and change files outside the project.
Allowing its port lets the VM out. Only allow a port for a server you'd trust
with whatever the VM asks of it. For your IDE, turn on [`ide`](#ide)
instead of allowing its port.

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

The size is fixed when the first run creates the disk. Changing it
later has no effect, until you start the VM over with
[`aibox projects reset`](usage.md#starting-over), which makes a new disk.

## mounts

Folders on your machine that the VM can read, but not write. Write them as
`host:guest`.
`guest` is the path in the VM. A host path starting with `~/` starts in your
home folder.

- The guest path has to stay clear of the folders the VM needs: `/project`,
  `/dev`, `/proc`, `/sys`, `/run`, `/tmp`, `/etc`, `/bin`, `/sbin`, `/lib`,
  `/lib64`, `/usr` and `/root`. It also can't overlap with other mounts.
- The host folder can't be your home folder or a folder above it, because
  the VM would see your keys and shell files.
- The host folder can't be `~/.aibox`, a folder in it or a folder above it,
  because the VM would see every project's config and Claude Code login.
- aibox compares folders rather than paths for these two rules, so a
  symlink or another spelling doesn't get around them.
- A mount inside `/home/user` is fine. The VM's home is a folder aibox
  manages, not your home folder. Leave `/home/user/.claude` itself alone,
  because the VM's login lives there.
- The VM caches names and attributes for the whole run. If you change the
  folder on your machine, a running VM may not see it.
- On macOS the VM runs arm64 Linux, so Mac programs don't run in it.
- The host folder can't be the root of a drive or share either.
- On Windows write the host folder with its drive, like
  `C:\Users\me\sdk:/opt/go`, or with forward slashes, `C:/Users/me/sdk:/opt/go`.
  `~\` works like `~/`. Don't put the entry in double quotes, because YAML
  reads backslashes in double quotes as escapes; single quotes are fine.

## skills

If you have skills in `~/.claude/skills`, aibox mounts them read-only at the
same place in the VM's home, so Claude Code finds them. That's the default,
`skills: all`. To keep your skills out of the VM, set:

```yaml
skills: none
```

To give the VM only some skills, mount them inside
`/home/user/.claude/skills`. A mount there takes the place of the whole
folder:

```yaml
mounts:
  - ~/.claude/skills/pdf:/home/user/.claude/skills/pdf
```

A symlink in the skills folder that points outside it doesn't work in the
VM.

`aibox run` and tasks get the same skills.

## agents, commands, claude-md, settings

The VM gets more of your Claude Code setup, so it works like on your
machine:

- `agents` and `commands`: your folders `~/.claude/agents` and
  `~/.claude/commands`, read-only, like your skills. Tasks get them too.
- `claude-md`: your `~/.claude/CLAUDE.md`. `aibox run` copies it into the
  VM's home at each start.
- `settings`: a few settings of your `~/.claude/settings.json`, like
  `model` and `theme`. `aibox run` puts them into the VM's own settings at
  each start, and keeps the others the VM has.

All four are `all` by default. To keep one out, set it to `none`:

```yaml
claude-md: none
settings: none
```

A mount inside `/home/user/.claude/agents` or `/home/user/.claude/commands`
takes the place of that folder, like with skills.

aibox doesn't remove what it put into the VM's home before. If you delete
your `CLAUDE.md` or a setting, or turn one of them off, the VM keeps the last
copy. [`aibox projects reset`](usage.md#starting-over) starts the VM's home
over. When the VM left something there that keeps a file from going in,
aibox says so and starts the VM anyway.

Only these settings come along: `model`, `theme`, `attribution`,
`includeCoAuthoredBy`, `outputStyle` and `alwaysThinkingEnabled`. Settings
that start a program or hold a secret stay on your machine: `hooks`, `env`,
`apiKeyHelper`, `statusLine` and the permissions. So do your plugins and
your MCP servers. The VM starts with its own.

A task starts with an empty home folder, so it doesn't get your
`CLAUDE.md` or your settings.

## ide

Claude Code can use the IDE that has your project open, like VS Code or
GoLand. It sees the code you select, shows its changes as a diff in the
editor and reads the errors the IDE reports. This is off by default, because aibox then passes
messages between the VM and a program on your machine. To turn it on, set:

```yaml
ide: true
```

aibox only passes on what's needed, and only for files in the project. Read
[In your IDE](usage.md#in-your-ide) before you turn it on, especially the
part about diffs.

With `ide` turned on, your `env` can't set `CLAUDE_CODE_SSE_PORT`, because
aibox sets it itself. Tasks never get the IDE, only `aibox run` does.

## clipboard

When you press Ctrl+V in Claude Code, it pastes the image on your clipboard.
The VM can read your clipboard only right after you press the key, and only
once. This is on by default. To turn it off, set:

```yaml
clipboard: false
```

The VM reaches the clipboard on port 3132 of its loopback, so with the
clipboard on, `allow` can't name that port. Tasks never get the clipboard,
only `aibox run` does. See [Pasting images](usage.md#pasting-images).

## path

Folders in the VM to put in front of its `PATH`, for Claude Code and the
shell. For example `/opt/go/bin` from the mount above.

An entry can also be the name of a variable, like `PATH` or `DIRENV_PATH`.
It then stands for the folders in that variable on your machine, without the
relative and empty ones. That way the VM finds the tools of a `nix develop`
or `direnv` shell, as long as `/nix/store` is mounted at the same path.
On Windows the folders of such a variable are Windows folders, which the VM
doesn't have, so it adds none of them.

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
