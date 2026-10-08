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
on `PATH` or at `/usr/libexec/virtiofsd`, as Debian 13, Ubuntu 24.04 and
Fedora have it. QEMU and virtiofsd from Nix work too: for a QEMU in
`/nix/store`, the sandbox of QEMU binds the store in place of `/usr/lib64`.
aibox also needs access to `/dev/kvm`.

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
the hosts and ports the proxy connected to or refused into `proxy.log`, each
once per run, both in the folder of the project below `~/.aibox/projects`.

## Tasks

`aibox task` runs Claude Code unattended on the last commit of the project,
in a VM that is thrown away afterwards. The commits it makes come back as a
git bundle, for you to review.

```
aibox task --model sonnet "fix the flaky test in internal/proxy"
```

- The VM gets a clone of `HEAD` with all its history, not your folder.
  `--from` names another branch, tag or commit to start from. Whatever it
  names goes into the VM with all its history, a stash or a commit you reset
  away too. A file you deleted in an earlier commit is still in that
  history. Changes you have not committed, files git ignores, and other
  branches and stashes that `--from` does not name are not part of the task.
  A shallow clone is refused, since it lacks the history.
- Claude Code works on the branch `aibox/task` with all permissions, since
  nobody is there to approve them. Git has your name and email from your git
  config. What Claude Code leaves uncommitted, apart from files git ignores,
  aibox commits in one more commit by `aibox <aibox@localhost>`.
- The VM starts with an empty home and an empty disk, and both are thrown
  away at the end. Your skills in `~/.claude/skills` are mounted read-only,
  as for `aibox run`. Other tasks and `aibox run` never see the home or the
  disk, so several tasks can run at once.
- The project config applies as for `aibox run`: `allow`, `mounts`, `path`,
  `env`, `memory`, `cpus` and `disk`. The hosts the proxy connected to or
  refused go into a `proxy.log` of the task, not into the one of the
  project.

A task can use everything the config gives it, and nobody stops it. It can
reach every host on the allow list, and every port of your machine that the
allow list names, and it can use every secret of `env`. With `GITHUB_TOKEN`
and `preset:github`, for example, it can push to your repositories. Claude
Code is told to push or open a pull request only when your prompt asks for
it, only to a repository whose URL your prompt names, and only to a new
branch, but nothing enforces that. A pushed branch or pull request can start
the CI of the repository, with its secrets. What Claude Code says it
published is its own word. aibox names the hosts the proxy connected to, but
not what went through those connections, so a push to `github.com` and a
fetch from it look the same. Text it reads, in the project or from the
network, can steer it. So before you run a task, take out of the config the
hosts, ports and secrets the task does not need.

| Flag | Meaning |
|---|---|
| `--from` | The branch, tag or commit the task starts from, `HEAD` by default. |
| `--file`, `-f` | A file whose text follows the arguments in the prompt, `-` for stdin. |
| `--model` | The model Claude Code uses. Without it, Claude Code picks its default. |
| `--max-turns` | The most turns Claude Code takes. |
| `--max-budget-usd` | The most Claude Code may spend by its own estimate, in US dollars at API prices. See below. |
| `--timeout` | How long Claude Code may work before it is stopped, 1h by default, at most 30 days. |

`--memory`, `--cpus`, `--image` and `--no-sandbox` work as for `aibox run`.

### Prompt from a file

A plan you wrote can be the prompt. `--file` puts the text of a file after
the arguments, and `--file -` reads stdin. Without arguments, aibox reads
the prompt from stdin, unless stdin is a terminal.

```
aibox task --file plan.md
aibox task --file plan.md "do only step 2"
cat plan.md | aibox task
```

- A relative path, and every link in it, must stay inside the folder you
  run aibox in, so that a link in a repository cannot make a task read
  another file of yours. For a plan elsewhere, give an absolute path. An
  absolute path must not end in a link.
- The file must be a plain file. For a pipe, use `--file -`.
- The prompt may be 1 MiB of UTF-8 text, the arguments and the file
  together.
- A plan that is not committed reaches the task only as its prompt, not as
  a file in `/project`.

### Login

The empty home has no login, so Claude Code logs in with a variable that the
`env` of the project config passes from your shell:

- `ANTHROPIC_API_KEY`, a key of the Claude Console. Usage is billed to it.
- `CLAUDE_CODE_OAUTH_TOKEN`, a token of your claude.ai subscription that
  `claude setup-token` makes.

```yaml
env:
  - CLAUDE_CODE_OAUTH_TOKEN
```

aibox never stores the token or the key. It takes the value your shell has
when the task starts.

An API key stays on your machine. The VM gets a placeholder in its place,
and `ANTHROPIC_BASE_URL` points Claude Code at a port on the loopback of the
VM. Behind that port, aibox sends each request to `api.anthropic.com` with
the real key. So nothing in the VM can read the key, send it away or write
it into a commit. But while the task runs, it can spend the key through
aibox, for any request to the Claude API. Give the key a spend limit in the
Console. An interactive Claude Code still reaches `api.anthropic.com` and
`platform.claude.com` directly when it starts, without the key, so keep them
on the allow list as a new config has them. Programs other than Claude Code
get the placeholder too, and work only if they follow `ANTHROPIC_BASE_URL`.
aibox refuses a config that sets `ANTHROPIC_BASE_URL` itself next to the
key.

A subscription token goes into the VM as it is, since Anthropic's terms let
no one but you handle it. In the VM, Claude Code and every program the task
runs can read it. A task that text has steered can send it to a host of the
allow list, or write it into a commit or the transcript. Use a token or key
for tasks only, so that you can revoke it on its own.

Whether unattended tasks are fine on your subscription is your own risk. See
Anthropic's [Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)
page. For many tasks, or tasks for a team, use an API key.

### Results

At the end aibox prints the hosts the VM connected to, how the task went,
the last message of Claude Code and the command that fetches the changes
into a branch of your repository:

```
17:09:25 aibox[259e07]: the VM connected to api.anthropic.com:443
17:09:25 aibox[259e07]: Claude Code exited with 0
17:09:25 aibox[259e07]: 4 turns in 8.321s, about 0.05 USD at API prices, ended by completed
17:09:25 vm[259e07]: | I added a "Purpose" section to the README and committed it as f3572af.
17:09:25 aibox[259e07]: the changes end at f3572af8c6df, fetch them with
17:09:25 aibox[259e07]:   git -c transfer.fsckObjects=true fetch .../changes.bundle aibox/task:aibox/task-20261007-170916-259e07
```

Every line of a task starts with the time, where the line comes from and the
end of the ID of the task, so that tasks that share a log can be told apart.
`vm` lines come from the VM and say what the task claims. `aibox` lines come
from the host.

The commits are untrusted until you have read them, like a pull request from
a stranger. Their authors prove nothing, since the task can set any author.

- Fetch them with the command aibox prints. aibox prints it only after it
  checked that the bundle carries `aibox/task` and nothing else, and needs no
  commit but the one the task started from. The fetch only adds that branch.
- Read the whole change with `git diff <commit> aibox/task-…`, whatever
  the single commits look like. Take `<commit>` from the line aibox
  prints, `aibox[…]: task … starts from …`, not from `result.json`, which the
  VM writes. That is the commit aibox checked the bundle against.
- Checking out, merging or rebasing the branch puts its files into your
  folder. Then everything in [What the VM writes](#what-the-vm-writes)
  applies.

The cost is Claude Code's own estimate, from the tokens it used at the list
prices of the API. With an API key it is close to what you are billed. With
a subscription you are not billed this amount, and the task counts against
the limits of your plan instead. `--max-budget-usd` caps the same estimate,
for Claude Code alone. Programs the task starts, such as another `claude`,
do not count. The only limit on time or spending that aibox enforces itself is `--timeout`. With an API key,
give the key a spend limit in the Console.

Each task has a folder `tasks/<start time>-<id>` in the folder of the
project below `~/.aibox/projects`. aibox prints the folder on stdout, for
scripts.

| File | What it holds |
|---|---|
| `changes.bundle` | The commits, empty when the task made none. |
| `result.json` | What the VM reports: the commit the task started from and the one it ended at, the exit code of Claude Code, whether it ran out of time, whether aibox committed leftovers, the files that were cut, warnings, the error of a failed step, and the last line of Claude Code with the cost and the turns. |
| `transcript.jsonl` | Everything Claude Code did, as `stream-json`, cut at 256 MiB. |
| `claude.log` | What Claude Code wrote to standard error, cut at 1 MiB. |
| `console.log` | The messages of the kernel and the init of the VM. |
| `proxy.log` | Each host and port the proxy connected to or refused for the task, once, with the time it first did, up to 1000 of each kind. aibox writes it, not the VM. It shows the name the VM asked for, not what went through. |
| `share/` | The prompt, the settings and the bundle of the commit the task started from, with all its history, until `aibox tasks clean` removes it. |
| `lock` | Locked while the task runs, so that `aibox tasks clean` leaves the task alone. |

aibox exits with 1 when the task did not finish. Claude Code failed or ran
out of time, a step in the VM failed, the VM was stopped, or the VM sent
results aibox does not take. The folder then holds what the task left.

### Removing old tasks

Every task keeps its folder, and with it a bundle of the history of the
project. aibox removes nothing on its own. `aibox tasks clean` removes the
bundles of the tasks that ended and keeps their results. Tasks that run stay
as they are.

```
aibox tasks clean
aibox tasks clean --all --older-than 720h
```

`--all` removes the whole folders of the tasks that ended, results
included. `--older-than` limits it to the tasks that started longer ago.
The command also removes what is left of tasks that failed as they started.
It names every folder it could not check or clean and then exits with 1.
A task counts as ended once its aibox ends. On macOS the VM ends with
aibox, even when aibox is killed. On Linux it should too, but that is not
yet tested with the real virtiofsd.

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

`ANTHROPIC_API_KEY` is the exception to passing values on. For `aibox run`
as for tasks, the key stays on your machine and the VM gets a placeholder,
see [Login](#login).

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
