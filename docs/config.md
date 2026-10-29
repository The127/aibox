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

For IPv6, aibox only takes internet addresses, the ones in `2000::/3`. Some
IPv6 addresses carry an IPv4 address, like NAT64 and 6to4 ones. aibox
checks that IPv4 address like any other. It only knows the standard NAT64
prefix, `64:ff9b::/96`. If your network has a NAT64 prefix of its own,
aibox can't tell which IPv4 address such an address leads to.

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
| `preset:cargo` | `crates.io`, `static.crates.io`, `index.crates.io`, `static.rust-lang.org`, `sh.rustup.rs` |
| `preset:github` | `github.com`, `api.github.com`, `codeload.github.com`, `*.githubusercontent.com` |
| `preset:mise` | `github.com`, `api.github.com`, `*.githubusercontent.com`, `mise-versions.jdx.dev`, `tuf-repo-cdn.sigstore.dev` |
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

To reach a server in the VM from your machine, it's the other way around.
See [ports](#ports).

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

## max-connections

How many connections the VM may have open through the proxy at once. The
default is 256:

```yaml
max-connections: 256
```

Each connection takes up a file handle of aibox, and aibox also needs
them for your API key, your git login and the rest. Without a limit, the
VM could open connections until aibox runs out of file handles. A browser
opens about 6 connections to a host and npm about 15, so a session stays
well below 256.

Beyond the limit, the proxy answers with `503 Service Unavailable` and
closes the connection, so the program in the VM gets an error instead of
waiting. `proxy.log` shows it as "too many connections at once".

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
- On Linux the VM has room for 20 shared folders. The project and the home
  take two, and the skills, agents and commands folders one each if you
  have them. That leaves 15 to 18 for mounts. With more, aibox stops before
  the VM starts and says so.
- On macOS the VM runs arm64 Linux, so Mac programs don't run in it.
- On Windows the VM runs amd64 Linux, so Windows programs don't run in it,
  and files made on Windows aren't executable there. Mount data, not tools,
  and install tools in the VM into `/usr/local`.
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

## mise

The VM has no compiler, no Node and no Python. If your project names its
tools in a `mise.toml` or `.tool-versions`, aibox can install them with
[mise](https://mise.jdx.dev) before Claude Code starts:

```yaml
mise: true
```

The first time, aibox shows you the two files and asks before it installs
anything. It asks again whenever one of them changes, since the VM can
change them too. If you say no, Claude Code starts without the tools.

The tools stay on the state disk, so the next start only installs what's
new. In the VM, `go`, `node` and the rest are on the `PATH`.

The downloads need their hosts on the allow list. mise looks up the
versions of most tools on GitHub, so allow `preset:mise` and the preset of
the tool, like `preset:go` for Go. `aibox config suggest` offers
`preset:mise` for a project with a `mise.toml` or `.tool-versions`.
`proxy.log` shows what the proxy refused.

Some things to know:

- aibox only reads `mise.toml` and `.tool-versions` in the project folder.
  Other mise files, like `.mise.toml` or one in `~/.config/mise`, don't
  count for the install before Claude Code starts.
- A task installs the tools too, but only if you approved the files of its
  commit in an `aibox run` before. A task can't ask you. It starts with an
  empty disk, so it downloads the tools on every run. The install may take
  as long as the task's `--timeout`, and Claude Code gets the whole
  timeout after it.
- Your approval covers what the two files say, not the files they point
  to, like a script that `[env]` runs. It doesn't cover mise's own files
  on the state disk either. The VM can change both, like anything else it
  keeps between runs. `aibox projects reset` clears the state disk.
- Folders of the [`path`](#path) setting come before the tools of mise on
  the `PATH`.
- mise runs in the VM, with the same limits as everything else there.

## clipboard

When you press Ctrl+V in Claude Code, or Alt+V on Windows, it pastes what's
on your clipboard: an image, which the VM can read only right after you
press the key, and only once, or text, which aibox types. This is on by
default. To turn it off, set:

```yaml
clipboard: false
```

The VM reaches the clipboard on port 3132 of its loopback, so with the
clipboard on, `allow` can't name that port. Tasks never get the clipboard,
only `aibox run` does. See [Pasting](usage.md#pasting).

## paste-alt-v

Whether Alt+V pastes too, as Ctrl+V does. Windows Terminal and the
Windows console paste on Ctrl+V themselves, so on Windows this is on, and
elsewhere off. To change that, set:

```yaml
paste-alt-v: false
```

## mode

`aibox run` shares your project folder with the VM, so Claude Code changes
your files right away. To give the VM a clone of the last commit instead,
and get its commits back as a branch to fetch when the session ends, set:

```yaml
mode: clone
```

`aibox run --mode mount` or `--mode clone` picks one for a single run,
whatever the config says. Read [Working on a clone](usage.md#working-on-a-clone)
for what differs. Tasks always work on a clone.

## name

Claude Code shows the folder it runs in at the top right of the prompt box
and in the terminal title. In the VM that's always `/project`, so every
session looks the same. To name the session after the project's path on
your machine instead, set:

```yaml
name: path
```

The session then shows `~/projects/my-app`, like Claude Code does outside
the VM. This is off by default, because the VM doesn't otherwise know where
your project is, and the path has your user name in it. Any other text is
the name itself:

```yaml
name: my app
```

`aibox run --name <name>`, or `-n`, names one run, whatever the config says,
and `--name ""` leaves one run without a name. A session you go on with
`--resume` keeps the name it has, unless you pass `--name`. Tasks never get
a name, only `aibox run` does. Your `env` can't set `AIBOX_SESSION_NAME`,
because aibox sets it itself.

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

## ports

When Claude Code starts a dev server in the VM, like `npm run dev`, you
want to open it in your browser. The VM has no network card, so you can't
reach it on its own. List the ports of its servers:

```yaml
ports:
  - 5173
  - 8080:3000
```

Then open `http://127.0.0.1:5173` on your machine for the server on port
5173 in the VM. `8080:3000` puts the VM's port 3000 on port 8080 of your
machine, for when 3000 is taken here.

Use `127.0.0.1` and not `localhost`. Your machine may look up `localhost` as
`[::1]` first, where aibox doesn't listen, and where another user could.

The server in the VM has to listen on `127.0.0.1` or `localhost`. That's
the default of most dev servers. aibox only listens on `127.0.0.1` on your
machine, never on your network.

aibox opens the ports before the VM starts. If another program already
holds one, aibox stops and tells you which. It refuses ports 3128 to 3132
of the VM, which aibox needs itself, and the loopback ports on your
[allow list](#ports-on-your-machine), which lead back to your machine.
Tasks never get the ports, only `aibox run` does.

Keep in mind what a port opens up:

- Any program and any user on your machine can connect to the port, not
  only your browser. Any website you visit can send requests to it too.
  Don't list a port that runs code or reads files for whoever asks, like
  the debugger port of Node, 9229.
- Your browser treats the VM's pages like those of any other server on
  `127.0.0.1` or `localhost`. Cookies don't keep ports apart, so a page
  from the VM can read and change the cookies of your other local servers
  on the same name. It can also make your browser send requests to them.
- What the VM's server sends is written by the VM. Treat it like a web
  page from a stranger.

### How it works

The terminal of `aibox run` is an SSH session between aibox and the VM's
init. aibox listens on `127.0.0.1` on your machine for each port. For each
connection it opens a channel in that SSH session, like `ssh -L` does. The
init connects the channel to the port on `127.0.0.1` in the VM, and to no
other address. The ports close when the session ends.

aibox locks itself down once the VM runs, so it can't open a port after
that. That's why the ports come from the config and can't follow what the
VM opens.

## watch

When an `aibox run` session ends, aibox tells you which files the VM may
have changed that programs on your machine run without asking, like
`.git/config` or `.envrc`. It always checks
[these files](what-the-vm-writes.md#what-aibox-checks-for-you). If your
project has more of them, add them:

```yaml
watch:
  - Makefile
  - .github/workflows
```

An entry is a file or a folder of the project, written with `/`. For a
folder, aibox checks every file in it. aibox refuses an absolute path, one
that leads out of the project with `..`, and `.`, the whole project.

See [What the VM writes](what-the-vm-writes.md#what-aibox-checks-for-you)
for what the check catches and what it doesn't.
